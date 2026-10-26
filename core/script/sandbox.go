package script

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/uidbz/anneal/core/paths"

	"github.com/deepnoodle-ai/risor/v2/pkg/object"
)

// The fs module is the sandbox's only filesystem surface, and it is
// confined: reads may reach the workdir and the anneal state root
// (~/.anneal — scripts can analyze session logs and harness files),
// writes only the workdir and ~/.anneal/scratch. Symlinks are resolved
// before the containment check, and secret-bearing dot-directories are
// denied regardless of roots. Command execution and the network are not
// reachable from fs at all.
//
// Denials are returned as Go errors from the builtins: the Risor v2 VM
// raises them as exceptions and aborts the script, so a denied call can
// never be ignored as a value.

// maxListEntries caps one fs.list result.
const maxListEntries = 1000

// denySegments are never readable or writable through fs, wherever they
// live.
var denySegments = []string{".ssh", ".aws", ".gnupg", ".azure", ".kube", ".docker"}

type policy struct {
	workdir string
	root    string // anneal state dir: read-only through fs
	scratch string // root/scratch: writable
	home    string
}

func newPolicy(workdir string) *policy {
	if workdir == "" {
		workdir, _ = os.Getwd()
	}
	root := paths.Root()
	p := &policy{workdir: workdir, root: root, scratch: filepath.Join(root, "scratch")}
	if h, err := os.UserHomeDir(); err == nil {
		p.home = h
	}
	return p
}

// resolve turns a script-supplied path into a clean absolute path with
// symlinks resolved (the target if it exists, otherwise its nearest
// existing parent) so containment cannot be evaded.
func (p *policy) resolve(raw string) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("empty path")
	}
	path := raw
	if !filepath.IsAbs(path) {
		path = filepath.Join(p.workdir, path)
	}
	path = filepath.Clean(path)
	if real, err := filepath.EvalSymlinks(path); err == nil {
		return filepath.Clean(real), nil
	}
	// The path itself may not exist yet (writes); resolve what does.
	dir, base := filepath.Split(path)
	if realDir, err := filepath.EvalSymlinks(filepath.Clean(dir)); err == nil {
		return filepath.Clean(filepath.Join(realDir, base)), nil
	}
	return path, nil
}

// under reports whether path sits at or below root (roots are resolved
// too, so a symlinked root cannot break containment).
func under(path, root string) bool {
	if rp, err := filepath.EvalSymlinks(root); err == nil {
		root = rp
	}
	return path == root || strings.HasPrefix(path, root+string(os.PathSeparator))
}

// denied reports whether any segment of path is a secret directory, or
// the path sits under the GitHub CLI credentials directory.
func (p *policy) denied(path string) bool {
	for _, seg := range strings.Split(path, string(os.PathSeparator)) {
		for _, d := range denySegments {
			if seg == d {
				return true
			}
		}
	}
	if p.home != "" {
		gh := filepath.Join(p.home, ".config", "gh")
		if path == gh || strings.HasPrefix(path, gh+string(os.PathSeparator)) {
			return true
		}
	}
	return false
}

func (p *policy) allowRead(path string) error {
	if p.denied(path) {
		return fmt.Errorf("path is outside the script sandbox")
	}
	if !under(path, p.workdir) && !under(path, p.root) {
		return fmt.Errorf("path is outside the readable roots (project dir and ~/.anneal)")
	}
	return nil
}

func (p *policy) allowWrite(path string) error {
	if p.denied(path) {
		return fmt.Errorf("path is outside the script sandbox")
	}
	if !under(path, p.workdir) && !under(path, p.scratch) {
		return fmt.Errorf("path is outside the writable roots (project dir and ~/.anneal/scratch)")
	}
	return nil
}

// fsArg extracts the n-th string argument.
func fsArg(op string, args []object.Object, n int) (string, error) {
	if len(args) < n+1 {
		return "", fmt.Errorf("fs.%s: expected %d argument(s)", op, n+1)
	}
	s, err := object.AsString(args[n])
	if err != nil {
		return "", fmt.Errorf("fs.%s: %s", op, err)
	}
	return s, nil
}

// fsPath resolves and checks one path argument against a checker.
func fsPath(op string, p *policy, args []object.Object, check func(string) error) (string, error) {
	raw, err := fsArg(op, args, 0)
	if err != nil {
		return "", err
	}
	path, err := p.resolve(raw)
	if err != nil {
		return "", fmt.Errorf("fs.%s: %s", op, err)
	}
	if err := check(path); err != nil {
		return "", fmt.Errorf("fs.%s: %s", op, err)
	}
	return path, nil
}

// fsModule builds the confined fs global.
func fsModule(p *policy) *object.Module {
	return object.NewBuiltinsModule("fs", map[string]object.Object{
		"read": object.NewBuiltin("fs.read", func(_ context.Context, args ...object.Object) (object.Object, error) {
			path, err := fsPath("read", p, args, p.allowRead)
			if err != nil {
				return nil, err
			}
			st, err := os.Stat(path)
			if err != nil {
				return nil, fmt.Errorf("fs.read: %s", err)
			}
			if st.IsDir() {
				return nil, fmt.Errorf("fs.read: %s is a directory (use fs.list)", path)
			}
			if st.Size() > MaxFileBytes {
				return nil, fmt.Errorf("fs.read: file is %d bytes, over the %d byte cap", st.Size(), MaxFileBytes)
			}
			data, err := os.ReadFile(path)
			if err != nil {
				return nil, fmt.Errorf("fs.read: %s", err)
			}
			return object.NewString(string(data)), nil
		}),
		"write": object.NewBuiltin("fs.write", func(_ context.Context, args ...object.Object) (object.Object, error) {
			path, err := fsPath("write", p, args, p.allowWrite)
			if err != nil {
				return nil, err
			}
			text, err := fsArg("write", args, 1)
			if err != nil {
				return nil, err
			}
			if len(text) > MaxFileBytes {
				return nil, fmt.Errorf("fs.write: content is %d bytes, over the %d byte cap", len(text), MaxFileBytes)
			}
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return nil, fmt.Errorf("fs.write: %s", err)
			}
			if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
				return nil, fmt.Errorf("fs.write: %s", err)
			}
			return object.NewBool(true), nil
		}),
		"list": object.NewBuiltin("fs.list", func(_ context.Context, args ...object.Object) (object.Object, error) {
			path, err := fsPath("list", p, args, p.allowRead)
			if err != nil {
				return nil, err
			}
			entries, err := os.ReadDir(path)
			if err != nil {
				return nil, fmt.Errorf("fs.list: %s", err)
			}
			items := make([]object.Object, 0, min(len(entries), maxListEntries))
			for i, e := range entries {
				if i >= maxListEntries {
					break
				}
				size := int64(0)
				if st, err := e.Info(); err == nil {
					size = st.Size()
				}
				items = append(items, object.NewMap(map[string]object.Object{
					"name": object.NewString(e.Name()),
					"size": object.NewInt(size),
					"dir":  object.NewBool(e.IsDir()),
				}))
			}
			return object.NewList(items), nil
		}),
		"exists": object.NewBuiltin("fs.exists", func(_ context.Context, args ...object.Object) (object.Object, error) {
			path, err := fsPath("exists", p, args, p.allowRead)
			if err != nil {
				return nil, err
			}
			_, err = os.Stat(path)
			if err != nil && !os.IsNotExist(err) {
				return nil, fmt.Errorf("fs.exists: %s", err)
			}
			return object.NewBool(err == nil), nil
		}),
		"remove": object.NewBuiltin("fs.remove", func(_ context.Context, args ...object.Object) (object.Object, error) {
			path, err := fsPath("remove", p, args, p.allowWrite)
			if err != nil {
				return nil, err
			}
			if err := os.Remove(path); err != nil {
				return nil, fmt.Errorf("fs.remove: %s", err)
			}
			return object.NewBool(true), nil
		}),
	})
}
