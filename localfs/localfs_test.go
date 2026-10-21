package localfs

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func setup(t *testing.T) (Dir, string) {
	t.Helper()
	root := t.TempDir()
	d, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	return d, d.Root()
}

func TestReadPathStaysInside(t *testing.T) {
	d, root := setup(t)
	if err := os.WriteFile(filepath.Join(root, "a.jpg"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if p, err := d.ReadPath("a.jpg"); err != nil || p != filepath.Join(root, "a.jpg") {
		t.Fatalf("got %q, %v", p, err)
	}
	for _, bad := range []string{"../a.jpg", "sub/../../a.jpg", "/etc/passwd", "", "."} {
		if _, err := d.ReadPath(bad); err == nil {
			t.Errorf("ReadPath(%q) should fail", bad)
		}
	}
}

func TestReadPathRefusesSymlinksOut(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlinks need privileges on Windows")
	}
	d, root := setup(t)
	outside := filepath.Join(t.TempDir(), "secret.txt")
	if err := os.WriteFile(outside, []byte("s"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(root, "link.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, err := d.ReadPath("link.jpg"); !errors.Is(err, ErrOutside) {
		t.Fatalf("err = %v, want ErrOutside", err)
	}
}

func TestCreateKeepsExistingFilesUnlessOverwrite(t *testing.T) {
	d, root := setup(t)
	f, target, err := d.Create("sub/dir/p.jpg", false)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(f, "one")
	_ = f.Close()
	if target != filepath.Join(root, "sub", "dir", "p.jpg") {
		t.Fatalf("target = %s", target)
	}
	if _, _, err := d.Create("sub/dir/p.jpg", false); err == nil {
		t.Fatal("an existing file must not be replaced without overwrite")
	}
	f, _, err = d.Create("sub/dir/p.jpg", true)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.WriteString(f, "two")
	_ = f.Close()
	if b, _ := os.ReadFile(target); string(b) != "two" {
		t.Fatalf("content = %q", b)
	}
}

func TestCreateRefusesEscapes(t *testing.T) {
	d, root := setup(t)
	for _, bad := range []string{"../x.jpg", "/tmp/x.jpg", "a/../../x.jpg"} {
		if _, _, err := d.Create(bad, true); err == nil {
			t.Errorf("Create(%q) should fail", bad)
		}
	}
	if runtime.GOOS == "windows" {
		return
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "out")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Create("out/x.jpg", true); !errors.Is(err, ErrOutside) {
		t.Fatalf("writing through a symlinked directory: err = %v", err)
	}
	target := filepath.Join(outside, "t.jpg")
	if err := os.WriteFile(target, []byte("keep"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, filepath.Join(root, "l.jpg")); err != nil {
		t.Fatal(err)
	}
	if _, _, err := d.Create("l.jpg", true); err == nil {
		t.Fatal("writing through a symlinked file must fail")
	}
	if b, _ := os.ReadFile(target); string(b) != "keep" {
		t.Fatal("the file outside was changed")
	}
}

func TestOpenNeedsADirectory(t *testing.T) {
	f := filepath.Join(t.TempDir(), "f")
	_ = os.WriteFile(f, nil, 0o600)
	if _, err := Open(f); err == nil {
		t.Fatal("a file is not a directory")
	}
	if _, err := Open(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("a missing directory must fail")
	}
}
