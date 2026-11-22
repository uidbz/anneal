package agent

import (
	"os"
	"strings"
	"testing"

	"github.com/uidbz/anneal/core/config"
)

func TestGentooAtomValidation(t *testing.T) {
	valid := []string{"app-misc/hello", "=sys-devel/gcc-15.2.0", ">=dev-lang/go-1.27", "app-misc/hello[doc]", "@world", "@system"}
	for _, s := range valid {
		if !validGentooAtom(s) {
			t.Errorf("%q rejected", s)
		}
	}
	invalid := []string{"", "-rf", "app-misc/hello;rm", "$(x)", "a b"}
	for _, s := range invalid {
		if validGentooAtom(s) {
			t.Errorf("%q accepted", s)
		}
	}
}

func TestGentooPreflightAndArgs(t *testing.T) {
	dir := t.TempDir()
	source := dir + "/source"
	overlay := dir + "/overlay"
	binpkg := dir + "/binpkgs"
	distfiles := dir + "/distfiles"
	for _, d := range []string{source + "/var/cache/binpkgs", source + "/var/cache/distfiles", overlay, binpkg, distfiles} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	root := config.GentooRoot{
		Name: "muslos", Source: source, OverlayDir: overlay,
		BinpkgDir: binpkg, DistfilesDir: distfiles,
	}
	r := &Runner{cfg: &config.Config{Gentoo: config.Gentoo{
		Enabled: true, BwrapPath: "/bin/true", TimeoutS: 10, TmpfsMB: 256,
		Roots: []config.GentooRoot{root},
	}}}
	text, err := r.gentooPreflight(root)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(text, "ACL mode") || !strings.Contains(text, "muslos") {
		t.Fatalf("preflight: %s", text)
	}
	p, err := r.gentooPaths(root)
	if err != nil {
		t.Fatal(err)
	}
	args, err := gentooBwrapArgs(r.cfg.Gentoo, root, p, "build", []string{"app-misc/hello"})
	if err != nil {
		t.Fatal(err)
	}
	joined := strings.Join(args, " ")
	for _, want := range []string{
		"--overlay " + overlay + "/upper " + overlay + "/work /",
		"--uid 0 --gid 0",
		"--bind " + binpkg + " /var/cache/binpkgs",
		"--bind " + distfiles + " /var/cache/distfiles",
		"--unshare-all",
		"--setenv FEATURES -userpriv -usersandbox buildpkg",
		"--buildpkgonly --oneshot app-misc/hello",
		"--uid 0",
	} {
		if !strings.Contains(joined, want) {
			t.Errorf("args missing %q\n%s", want, joined)
		}
	}
	if strings.Contains(joined, "/bin/sh") || strings.Contains(joined, "bash -c") {
		t.Fatalf("args use a shell: %s", joined)
	}
}

func TestLimitedBuffer(t *testing.T) {
	var b limitedBuffer
	b.max = 6
	b.Write([]byte("abcdef"))
	b.Write([]byte("gh"))
	if got := b.String(); got != "cdefgh" {
		t.Fatalf("buffer = %q", got)
	}
}
