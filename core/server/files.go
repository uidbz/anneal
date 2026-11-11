// Remote work-tree transfer (docs/PROTOCOL.md, "The work tree"): files.put
// and files.get move base64 tar.gz archives between a client and the
// instance's work root, and agent.send's optional work param selects a
// work-root-relative directory for one run. Every path on the wire is
// relative to the work root; absolute paths, drive letters and ".."
// segments are rejected, and archives are extracted with symlink/hardlink
// members refused and size/entry caps enforced.
package server

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
)

const (
	// maxExtractedBytes caps the total file content one archive carries,
	// in either direction (PROTOCOL.md: 64 MiB).
	maxExtractedBytes = 64 << 20
	// maxArchiveEntries caps tar members per archive.
	maxArchiveEntries = 2000
	// workDirPerm is how created work dirs are mode-bitted.
	workDirPerm = 0o755
)

// cleanWorkRel validates a wire path as work-root-relative and returns its
// cleaned slash form. It rejects empty paths, absolute paths (POSIX, and
// drive/UNC forms), NUL bytes, backslash separators (normalized to "/"
// first), and any ".." segment. The result is never "." — a path must
// name a location strictly inside the work root.
func cleanWorkRel(rel string) (string, error) {
	if rel == "" {
		return "", fmt.Errorf("path is empty")
	}
	if strings.ContainsRune(rel, 0) {
		return "", fmt.Errorf("path contains a NUL byte")
	}
	p := strings.ReplaceAll(rel, `\`, "/")
	if p == "" || strings.HasPrefix(p, "/") || path.IsAbs(p) ||
		filepath.VolumeName(filepath.FromSlash(p)) != "" || isDriveForm(p) {
		return "", fmt.Errorf("absolute path not allowed: %q", rel)
	}
	clean := path.Clean(p)
	if clean == "." {
		return "", fmt.Errorf("path %q must name a location inside the work root", rel)
	}
	for _, seg := range strings.Split(clean, "/") {
		if seg == ".." {
			return "", fmt.Errorf("path %q escapes the work root", rel)
		}
	}
	return clean, nil
}

// isDriveForm catches Windows drive-rooted paths ("C:/...") on platforms
// where filepath.VolumeName would not see them.
func isDriveForm(p string) bool {
	return len(p) >= 2 && p[1] == ':' &&
		((p[0] >= 'A' && p[0] <= 'Z') || (p[0] >= 'a' && p[0] <= 'z'))
}

// workRoot is the directory this instance runs its agent in — the one
// resolved at startup (anneald chdirs there before app.New).
func (s *Server) workRoot() string {
	return s.App.Runner.Cwd()
}

// resolveWorkPath maps a wire path to an absolute target inside the work
// root. The prefix check is belt-and-braces over cleanWorkRel.
func (s *Server) resolveWorkPath(rel string) (string, error) {
	clean, err := cleanWorkRel(rel)
	if err != nil {
		return "", err
	}
	root := s.workRoot()
	target := filepath.Join(root, filepath.FromSlash(clean))
	if target != root && !strings.HasPrefix(target, root+string(filepath.Separator)) {
		return "", fmt.Errorf("path %q escapes the work root", rel)
	}
	return target, nil
}

// prepareWorkDir resolves agent.send's work param, creating the directory
// (mkdir -p). The returned absolute path is what the runner chdirs into
// for the run.
func (s *Server) prepareWorkDir(rel string) (string, error) {
	target, err := s.resolveWorkPath(rel)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(target, workDirPerm); err != nil {
		return "", err
	}
	return target, nil
}

// extractTarGz unpacks a base64-decoded tar.gz under dest (created on
// demand). Members are validated like wire paths; symlinks, hardlinks and
// device members are refused outright (their Typeflag has no case below).
// Not transactional: a rejected member can leave earlier members applied;
// PROTOCOL.md states this.
func extractTarGz(dest string, data []byte) (int, error) {
	zr, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return 0, fmt.Errorf("gunzip: %w", err)
	}
	defer zr.Close()
	if err := os.MkdirAll(dest, workDirPerm); err != nil {
		return 0, err
	}
	tr := tar.NewReader(zr)
	entries, total := 0, int64(0)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return entries, fmt.Errorf("tar: %w", err)
		}
		entries++
		if entries > maxArchiveEntries {
			return entries, fmt.Errorf("archive has more than %d entries", maxArchiveEntries)
		}
		if hdr.Name == "." || hdr.Name == "./" || hdr.Name == "" {
			continue // the extraction root itself
		}
		clean, err := cleanWorkRel(hdr.Name)
		if err != nil {
			return entries, fmt.Errorf("archive entry %q: %w", hdr.Name, err)
		}
		target := filepath.Join(dest, filepath.FromSlash(clean))
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, workDirPerm); err != nil {
				return entries, err
			}
		case tar.TypeReg:
			total += hdr.Size
			if total > maxExtractedBytes {
				return entries, fmt.Errorf("archive extracts more than %d bytes", maxExtractedBytes)
			}
			if err := os.MkdirAll(filepath.Dir(target), workDirPerm); err != nil {
				return entries, err
			}
			mode := os.FileMode(hdr.Mode) & 0o777
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
			if err != nil {
				return entries, err
			}
			// The size cap is enforced on bytes actually copied, not the
			// header (a lying header must not slip a huge file in).
			n, err := io.Copy(f, io.LimitReader(tr, maxExtractedBytes+1))
			closeErr := f.Close()
			if err != nil {
				return entries, err
			}
			if closeErr != nil {
				return entries, closeErr
			}
			if n > maxExtractedBytes {
				return entries, fmt.Errorf("archive extracts more than %d bytes", maxExtractedBytes)
			}
		default: // symlink, hardlink, device, fifo...: refused
			return entries, fmt.Errorf("archive entry %q: unsupported type %q", hdr.Name, string(rune(hdr.Typeflag)))
		}
	}
	return entries, nil
}

// archiveTarGz packs a work-root subtree into a tar.gz whose member names
// are relative to the packed path (the inverse of extractTarGz). A single
// regular file yields one member named after its base name — restoring it
// means putting to the file's parent directory. Symlinks encountered in a
// walked tree abort the archive rather than leak targets outside the root.
func archiveTarGz(target string) ([]byte, error) {
	st, err := os.Lstat(target)
	if err != nil {
		return nil, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return nil, fmt.Errorf("%q is a symlink", target)
	}
	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)
	total, entries := int64(0), 0

	writeFile := func(name, full string, fsys fs.FileInfo) error {
		entries++
		if entries > maxArchiveEntries {
			return fmt.Errorf("tree has more than %d files", maxArchiveEntries)
		}
		total += fsys.Size()
		if total > maxExtractedBytes {
			return fmt.Errorf("tree is larger than %d bytes", maxExtractedBytes)
		}
		hdr, err := tar.FileInfoHeader(fsys, "")
		if err != nil {
			return err
		}
		hdr.Name = name
		if err := tw.WriteHeader(hdr); err != nil {
			return err
		}
		f, err := os.Open(full)
		if err != nil {
			return err
		}
		defer f.Close()
		_, err = io.Copy(tw, f)
		return err
	}

	if !st.IsDir() {
		if !st.Mode().IsRegular() {
			return nil, fmt.Errorf("%q is not a regular file or directory", target)
		}
		if err := writeFile(path.Base(target), target, st); err != nil {
			return nil, err
		}
	} else {
		if err := filepath.WalkDir(target, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(target, p)
			if err != nil {
				return err
			}
			name := filepath.ToSlash(rel)
			if name == "." {
				return nil // the packed root itself
			}
			if d.Type()&os.ModeSymlink != 0 {
				return fmt.Errorf("%q is a symlink", name)
			}
			if d.IsDir() {
				entries++
				if entries > maxArchiveEntries {
					return fmt.Errorf("tree has more than %d files", maxArchiveEntries)
				}
				info, err := d.Info()
				if err != nil {
					return err
				}
				hdr, err := tar.FileInfoHeader(info, "")
				if err != nil {
					return err
				}
				hdr.Name = name + "/"
				return tw.WriteHeader(hdr)
			}
			if !d.Type().IsRegular() {
				return fmt.Errorf("%q is not a regular file", name)
			}
			info, err := d.Info()
			if err != nil {
				return err
			}
			return writeFile(name, p, info)
		}); err != nil {
			return nil, err
		}
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gw.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// ---- RPC handlers ----------------------------------------------------------

// files.put {path,data}: extract a base64 tar.gz under path (members are
// relative to path; parent dirs created). Inbound frames cap at 1 MiB
// (protocol maxFrame), so the base64 payload must fit inside that.
func (s *Server) filesPut(params json.RawMessage) (any, error) {
	var p struct {
		Path string `json:"path"`
		Data string `json:"data"`
	}
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	target, err := s.resolveWorkPath(p.Path)
	if err != nil {
		return nil, argErr(err)
	}
	data, err := base64.StdEncoding.DecodeString(p.Data)
	if err != nil {
		return nil, argErr(fmt.Errorf("data is not base64: %w", err))
	}
	entries, err := extractTarGz(target, data)
	if err != nil {
		return nil, &wrapped{codeHandler, err}
	}
	return map[string]any{"ok": true, "path": p.Path, "entries": entries}, nil
}

// files.get {path}: archive path into a base64 tar.gz (members relative
// to path; a single file yields one member named after its base name).
// Outbound frames are not size-capped, but the archive obeys the same
// content/entry caps as extraction.
func (s *Server) filesGet(params json.RawMessage) (any, error) {
	var p struct {
		Path string `json:"path"`
	}
	if err := decode(params, &p); err != nil {
		return nil, err
	}
	target, err := s.resolveWorkPath(p.Path)
	if err != nil {
		return nil, argErr(err)
	}
	data, err := archiveTarGz(target)
	if err != nil {
		return nil, &wrapped{codeHandler, err}
	}
	return map[string]any{
		"ok":    true,
		"path":  p.Path,
		"data":  base64.StdEncoding.EncodeToString(data),
		"bytes": len(data),
	}, nil
}
