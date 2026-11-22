package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/uidbz/anneal/core/config"
	"github.com/uidbz/anneal/core/paths"

	"github.com/deepnoodle-ai/dive"
)

// gentooAtomRe accepts normal Portage atoms, including version operators
// (=, >=, <=, ~) and use-dependency suffixes, but no shell metacharacters.
var gentooAtomRe = regexp.MustCompile(`^(?:[<>=~!]+)?[A-Za-z0-9][A-Za-z0-9+_.:/-]*(\[[A-Za-z0-9_,!?+-]+\])?$`)

type gentooInput struct {
	Action   string   `json:"action" description:"preflight, list, info, pretend, or build" enum:"preflight,list,info,pretend,build"`
	Root     string   `json:"root,omitempty" description:"configured root name (for actions other than list)"`
	Packages []string `json:"packages,omitempty" description:"package atoms, e.g. app-misc/hello"`
}

func (r *Runner) gentooTool() dive.Tool {
	return dive.FuncTool("gentoo",
		"Compile Gentoo packages in an isolated, rootless chroot using bubblewrap. Actions: preflight checks the root and bwrap; list shows configured roots; info runs emerge --info; pretend resolves atoms; build runs emerge --buildpkgonly --oneshot and publishes .gpkg.tar files to the configured binhost. No shell is used and package atoms are validated. The agent never runs as root: the sandbox uses a user namespace, a user-owned clone or ACL-enabled tree, and an overlay so the real chroot stays unchanged.",
		func(ctx context.Context, in *gentooInput) (*dive.ToolResult, error) {
			if !r.cfg.Gentoo.Enabled {
				return dive.NewToolResultError("gentoo: disabled in config (gentoo.enabled)"), nil
			}
			action := strings.ToLower(strings.TrimSpace(in.Action))
			if action == "" {
				action = "preflight"
			}
			if action == "list" {
				return dive.NewToolResultText(r.gentooRootList()), nil
			}
			root, ok := r.gentooRoot(in.Root)
			if !ok {
				return dive.NewToolResultError(fmt.Sprintf("gentoo: unknown root %q (configured: %s)", in.Root, r.gentooRootNames())), nil
			}
			switch action {
			case "preflight":
				text, err := r.gentooPreflight(root)
				if err != nil {
					return dive.NewToolResultError("gentoo preflight: " + err.Error()), nil
				}
				return dive.NewToolResultText(text), nil
			case "info":
				return r.gentooRun(ctx, root, "info", nil), nil
			case "pretend", "build":
				if len(in.Packages) == 0 {
					return dive.NewToolResultError("gentoo: packages are required for " + action), nil
				}
				for _, p := range in.Packages {
					if !validGentooAtom(p) {
						return dive.NewToolResultError("gentoo: invalid package atom " + strconv.Quote(p)), nil
					}
				}
				return r.gentooRun(ctx, root, action, in.Packages), nil
			default:
				return dive.NewToolResultError("gentoo: action must be preflight|list|info|pretend|build"), nil
			}
		},
		dive.WithFuncToolAnnotations(&dive.ToolAnnotations{Title: "Gentoo chroot build"}))
}

func validGentooAtom(s string) bool {
	s = strings.TrimSpace(s)
	if s == "@world" || s == "@system" {
		return true
	}
	if s == "" || strings.HasPrefix(s, "-") {
		return false
	}
	return gentooAtomRe.MatchString(s)
}

func (r *Runner) gentooRoot(name string) (config.GentooRoot, bool) {
	for _, g := range r.cfg.Gentoo.Roots {
		if strings.EqualFold(g.Name, name) {
			return g, true
		}
	}
	return config.GentooRoot{}, false
}

func (r *Runner) gentooRootNames() string {
	var names []string
	for _, g := range r.cfg.Gentoo.Roots {
		names = append(names, g.Name)
	}
	if len(names) == 0 {
		return "(none)"
	}
	return strings.Join(names, ", ")
}

func (r *Runner) gentooRootList() string {
	if len(r.cfg.Gentoo.Roots) == 0 {
		return "no gentoo roots configured"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d gentoo roots:\n", len(r.cfg.Gentoo.Roots))
	for _, g := range r.cfg.Gentoo.Roots {
		fmt.Fprintf(&sb, "- %s: source=%s base=%s binpkg=%s\n", g.Name, g.Source, firstNonEmpty(g.Base, "(ACL mode)"), g.BinpkgDir)
	}
	return sb.String()
}

// gentooPaths resolves defaults for one root.
type gentooPaths struct {
	bwrap      string
	source     string
	lower      string
	overlayDir string
	upper      string
	work       string
	pkgDir     string
	distDir    string
	binpkgDir  string
	distfiles  string
}

func (r *Runner) gentooPaths(root config.GentooRoot) (gentooPaths, error) {
	if strings.TrimSpace(root.Name) == "" {
		return gentooPaths{}, fmt.Errorf("root name is empty")
	}
	if strings.TrimSpace(root.Source) == "" {
		return gentooPaths{}, fmt.Errorf("root %q: source is required", root.Name)
	}
	p := gentooPaths{
		bwrap:      firstNonEmpty(r.cfg.Gentoo.BwrapPath, "bwrap"),
		source:     root.Source,
		lower:      firstNonEmpty(root.Base, root.Source),
		overlayDir: firstNonEmpty(root.OverlayDir, filepath.Join(paths.Gentoo(), root.Name)),
		pkgDir:     firstNonEmpty(root.PkgDir, "/var/cache/binpkgs"),
		distDir:    firstNonEmpty(root.DistDir, "/var/cache/distfiles"),
		binpkgDir:  root.BinpkgDir,
		distfiles:  root.DistfilesDir,
	}
	p.upper = filepath.Join(p.overlayDir, "upper")
	p.work = filepath.Join(p.overlayDir, "work")
	if p.overlayDir == "/" || p.work == "/" || filepath.Base(p.work) != "work" {
		return p, fmt.Errorf("root %q: unsafe overlay_dir %q", root.Name, p.overlayDir)
	}
	if p.binpkgDir == "" {
		return p, fmt.Errorf("root %q: binpkg_dir is required (the LAN binhost output directory)", root.Name)
	}
	if p.distfiles == "" {
		p.distfiles = filepath.Join(p.overlayDir, "distfiles")
	}
	if p.pkgDir == "/" || p.distDir == "/" {
		return p, fmt.Errorf("root %q: pkgdir/distdir must be subdirectories, not /", root.Name)
	}
	if !strings.HasPrefix(p.pkgDir, "/") || !strings.HasPrefix(p.distDir, "/") {
		return p, fmt.Errorf("root %q: pkgdir/distdir must be absolute paths inside the chroot", root.Name)
	}
	return p, nil
}

func (r *Runner) gentooPreflight(root config.GentooRoot) (string, error) {
	p, err := r.gentooPaths(root)
	if err != nil {
		return "", err
	}
	bwrap, err := exec.LookPath(p.bwrap)
	if err != nil {
		return "", fmt.Errorf("bubblewrap not found (%q): install sys-apps/bubblewrap; unprivileged user namespaces are required", p.bwrap)
	}
	if st, err := os.Stat(p.source); err != nil || !st.IsDir() {
		return "", fmt.Errorf("source %s is not a directory", p.source)
	}
	if st, err := os.Stat(p.lower); err != nil || !st.IsDir() {
		return "", fmt.Errorf("lower %s is not a directory", p.lower)
	}
	for _, a := range root.EmergeArgs {
		if !strings.HasPrefix(a, "-") || strings.ContainsAny(a, "\x00\r\n") {
			return "", fmt.Errorf("invalid emerge_args entry %q (flags must start with -)", a)
		}
	}
	if root.Base == "" {
		// ACL mode: the real tree must be writable by the anneal user,
		// otherwise overlayfs cannot copy up changed files.
		if err := writablePath(p.lower); err != nil {
			return "", fmt.Errorf("ACL mode requires %s to be writable by %s (%v); either grant ACLs/default ACLs to the anneal user or set base to a user-owned reflink clone", p.lower, currentUser(), err)
		}
	} else if err := writablePath(p.lower); err != nil {
		return "", fmt.Errorf("clone %s is not writable (%v)", p.lower, err)
	}
	for _, dir := range []string{p.upper, p.work, p.binpkgDir, p.distfiles} {
		if err := writableDir(dir); err != nil {
			return "", err
		}
	}
	for _, inner := range []string{p.pkgDir, p.distDir} {
		if st, err := os.Stat(filepath.Join(p.lower, filepath.FromSlash(strings.TrimPrefix(inner, "/")))); err != nil || !st.IsDir() {
			return "", fmt.Errorf("chroot path %s does not exist in %s; create it or set pkgdir/distdir", inner, p.lower)
		}
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "root %s: OK\n", root.Name)
	fmt.Fprintf(&sb, "  bwrap: %s\n", bwrap)
	fmt.Fprintf(&sb, "  source (read-only lower): %s\n", p.source)
	if root.Base != "" {
		fmt.Fprintf(&sb, "  writable clone: %s\n", p.lower)
	} else {
		fmt.Fprintf(&sb, "  ACL mode (writable tree): %s\n", p.lower)
	}
	fmt.Fprintf(&sb, "  overlay: %s\n", p.overlayDir)
	fmt.Fprintf(&sb, "  binpkgs: %s -> %s\n", p.binpkgDir, p.pkgDir)
	fmt.Fprintf(&sb, "  distfiles: %s -> %s\n", p.distfiles, p.distDir)
	fmt.Fprintf(&sb, "  network: %v\n", !root.NoNetwork)
	return sb.String(), nil
}

func (r *Runner) gentooRun(ctx context.Context, root config.GentooRoot, action string, packages []string) *dive.ToolResult {
	p, err := r.gentooPaths(root)
	if err != nil {
		return dive.NewToolResultError("gentoo: " + err.Error())
	}
	if _, err := r.gentooPreflight(root); err != nil {
		return dive.NewToolResultError("gentoo: " + err.Error())
	}
	// overlayfs requires an empty work directory; a crashed previous run can
	// leave index files there. The upper (build changes) is preserved.
	if err := os.RemoveAll(p.work); err != nil {
		return dive.NewToolResultError("gentoo: clean overlay work dir: " + err.Error())
	}
	if err := os.MkdirAll(p.work, 0o755); err != nil {
		return dive.NewToolResultError("gentoo: recreate overlay work dir: " + err.Error())
	}
	args, err := gentooBwrapArgs(r.cfg.Gentoo, root, p, action, packages)
	if err != nil {
		return dive.NewToolResultError("gentoo: " + err.Error())
	}
	timeout := time.Duration(r.cfg.Gentoo.TimeoutS) * time.Second
	if timeout <= 0 {
		timeout = 2 * time.Hour
	}
	runCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	r.emit(map[string]any{"type": "gentoo.start", "action": action, "root": root.Name, "packages": packages})
	start := time.Now()
	cmd := exec.CommandContext(runCtx, p.bwrap, args...)
	var out limitedBuffer
	out.max = 1 << 20
	cmd.Stdout = &out
	cmd.Stderr = &out
	runErr := cmd.Run()
	duration := time.Since(start)
	status := "ok"
	if runCtx.Err() == context.DeadlineExceeded {
		status = "timeout"
	} else if runErr != nil {
		status = "failed"
	}
	newPkgs := []string{}
	if action == "build" {
		newPkgs = listNewPackages(p.binpkgDir, start)
	}
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	appendGentooAudit(map[string]any{
		"at": time.Now().UTC(), "action": action, "root": root.Name,
		"packages": packages, "status": status, "error": errText,
		"duration_s": int(duration.Seconds()), "binpkgs": newPkgs,
	})
	r.emit(map[string]any{"type": "gentoo.done", "action": action, "root": root.Name,
		"status": status, "error": errText, "binpkgs": newPkgs})

	var sb strings.Builder
	fmt.Fprintf(&sb, "%s %s: %s (%s)\n", root.Name, action, status, duration.Round(time.Second))
	if len(packages) > 0 {
		fmt.Fprintf(&sb, "packages: %s\n", strings.Join(packages, " "))
	}
	body := out.String()
	if body != "" {
		sb.WriteString("\n")
		sb.WriteString(body)
	}
	if len(newPkgs) > 0 {
		sb.WriteString("\nnew binary packages:\n")
		for _, f := range newPkgs {
			if st, err := os.Stat(f); err == nil {
				fmt.Fprintf(&sb, "- %s (%s)\n", filepath.Base(f), humanSize(st.Size()))
			} else {
				fmt.Fprintf(&sb, "- %s\n", filepath.Base(f))
			}
		}
	}
	if runErr != nil {
		fmt.Fprintf(&sb, "\nerror: %v\n", runErr)
	}
	return dive.NewToolResultText(sb.String())
}

func gentooBwrapArgs(g config.Gentoo, root config.GentooRoot, p gentooPaths, action string, packages []string) ([]string, error) {
	features := firstNonEmpty(root.Features, "-userpriv -usersandbox buildpkg")
	tmpMB := g.TmpfsMB
	if tmpMB <= 0 {
		tmpMB = 16384
	}
	args := []string{
		"--overlay", p.upper, p.work, "/",
		"--uid", "0", "--gid", "0",
		"--proc", "/proc",
		"--dev", "/dev",
		"--size", strconv.FormatInt(int64(tmpMB)<<20, 10), "--tmpfs", "/dev/shm",
		"--bind", p.binpkgDir, p.pkgDir,
		"--bind", p.distfiles, p.distDir,
		"--unshare-all",
		"--disable-userns",
	}
	if !root.NoNetwork {
		args = append(args, "--share-net")
		if _, err := os.Stat("/etc/resolv.conf"); err == nil {
			args = append(args, "--ro-bind-try", "/etc/resolv.conf", "/etc/resolv.conf")
		}
	}
	args = append(args,
		"--die-with-parent",
		"--new-session",
		"--hostname", "anneal-gentoo-"+root.Name,
		"--clearenv",
		"--setenv", "PATH", "/usr/sbin:/usr/bin:/sbin:/bin",
		"--setenv", "HOME", "/root",
		"--setenv", "LC_ALL", "C",
		"--setenv", "PORTAGE_USERNAME", "root",
		"--setenv", "PORTAGE_GRPNAME", "root",
		"--setenv", "PORTAGE_TMPDIR", "/dev/shm",
		"--setenv", "PORTAGE_TMPFS", "/dev/shm",
		"--setenv", "BUILD_PREFIX", "/dev/shm",
		"--setenv", "FEATURES", features,
		"--chdir", "/",
	)
	keys := sortedKeys(root.Env)
	for _, k := range keys {
		args = append(args, "--setenv", k, root.Env[k])
	}
	args = append(args, "/usr/bin/emerge")
	switch action {
	case "info":
		args = append(args, "--info")
	case "pretend":
		args = append(args, "--pretend", "--buildpkgonly")
		args = append(args, root.EmergeArgs...)
		args = append(args, packages...)
	case "build":
		args = append(args, "--buildpkgonly", "--oneshot")
		args = append(args, root.EmergeArgs...)
		args = append(args, packages...)
	default:
		return nil, fmt.Errorf("unknown action %q", action)
	}
	return args, nil
}

func writableDir(dir string) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	f, err := os.CreateTemp(dir, ".anneal-write-*")
	if err != nil {
		return fmt.Errorf("%s is not writable: %w", dir, err)
	}
	name := f.Name()
	_ = f.Close()
	_ = os.Remove(name)
	return nil
}

// writablePath tests a directory without creating a persistent file.
func writablePath(dir string) error {
	f, err := os.CreateTemp(dir, ".anneal-perm-*")
	if err != nil {
		return err
	}
	name := f.Name()
	_ = f.Close()
	return os.Remove(name)
}

func listNewPackages(dir string, since time.Time) []string {
	var out []string
	_ = filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, ".gpkg.tar") && !strings.HasSuffix(name, ".tbz2") {
			return nil
		}
		info, err := d.Info()
		if err != nil || info.ModTime().Before(since.Add(-2*time.Second)) {
			return nil
		}
		out = append(out, path)
		if len(out) >= 200 {
			return filepath.SkipAll
		}
		return nil
	})
	return out
}

func appendGentooAudit(rec map[string]any) {
	dir := paths.Gentoo()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return
	}
	data, err := json.Marshal(rec)
	if err != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "audit.jsonl"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	defer f.Close()
	_, _ = f.Write(append(data, '\n'))
}

type limitedBuffer struct {
	max int
	b   []byte
}

func (w *limitedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if w.max <= 0 {
		w.max = 1 << 20
	}
	if len(p) >= w.max {
		w.b = append(w.b[:0], p[len(p)-w.max:]...)
		return n, nil
	}
	if len(w.b)+len(p) > w.max {
		drop := len(w.b) + len(p) - w.max
		w.b = append(w.b[:0], w.b[drop:]...)
	}
	w.b = append(w.b, p...)
	return n, nil
}

func (w *limitedBuffer) String() string { return string(w.b) }

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 5 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %ciB", float64(n)/float64(div), "KMGTPE"[exp])
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	// Small maps: insertion sort avoids another import.
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j] < out[j-1]; j-- {
			out[j], out[j-1] = out[j-1], out[j]
		}
	}
	return out
}

func currentUser() string {
	if u := os.Getenv("USER"); u != "" {
		return u
	}
	return "the anneal user"
}
