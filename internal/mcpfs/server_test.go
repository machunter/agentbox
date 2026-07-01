package mcpfs

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeResolveJail(t *testing.T) {
	root := filepath.Clean(t.TempDir())

	ok := []struct{ in, want string }{
		{"", root},
		{".", root},
		{"file.txt", filepath.Join(root, "file.txt")},
		{"sub/dir/x", filepath.Join(root, "sub/dir/x")},
		{"sub/../file.txt", filepath.Join(root, "file.txt")}, // normalized, stays inside
	}
	for _, c := range ok {
		got, err := safeResolve(root, c.in)
		if err != nil {
			t.Errorf("safeResolve(%q) unexpected error: %v", c.in, err)
			continue
		}
		if got != c.want {
			t.Errorf("safeResolve(%q) = %q, want %q", c.in, got, c.want)
		}
	}

	// Paths that genuinely resolve outside the root must be rejected. (Note an
	// absolute input like "/etc/passwd" is reinterpreted as root-relative, and
	// "/x/../../.." collapses to the root itself — those are safe, not escapes.)
	escapes := []string{"../etc/passwd", "../../secret", "sub/../../outside"}
	for _, in := range escapes {
		if _, err := safeResolve(root, in); err == nil {
			t.Errorf("safeResolve(%q) should have been rejected as an escape", in)
		}
	}
}

func TestSafeResolveRejectsSymlinkEscape(t *testing.T) {
	root := filepath.Clean(t.TempDir())
	// A secret living outside the jail.
	outside := filepath.Join(t.TempDir(), "secret.txt")
	mustWrite(t, outside, "top secret")

	// A symlink inside the jail that points at the outside secret — the kind of
	// thing run_bash could create in the shared container.
	link := filepath.Join(root, "escape")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatalf("symlink: %v", err)
	}

	if _, err := safeResolve(root, "escape"); err == nil {
		t.Error("safeResolve should reject a symlink pointing outside the root")
	}
	if _, err := readFile(root, "escape"); err == nil {
		t.Error("readFile should refuse to follow a symlink out of the jail")
	}

	// A symlink pointing to an in-jail file is fine.
	mustWrite(t, filepath.Join(root, "real.txt"), "ok")
	if err := os.Symlink(filepath.Join(root, "real.txt"), filepath.Join(root, "inside")); err != nil {
		t.Fatalf("symlink: %v", err)
	}
	if out, err := readFile(root, "inside"); err != nil || out != "ok" {
		t.Errorf("readFile(inside) = %q, %v; want %q, nil", out, err, "ok")
	}
}

func TestListDir(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "a.txt"), "hello")
	if err := os.Mkdir(filepath.Join(root, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}

	out, err := listDir(root, "")
	if err != nil {
		t.Fatalf("listDir: %v", err)
	}
	if !strings.Contains(out, "a.txt") || !strings.Contains(out, "sub/") {
		t.Fatalf("listing missing entries: %q", out)
	}

	if _, err := listDir(root, "../.."); err == nil {
		t.Error("listDir should reject an escaping path")
	}
}

func TestReadFile(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "note.txt"), "the codename is Bluefin")

	out, err := readFile(root, "note.txt")
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if out != "the codename is Bluefin" {
		t.Fatalf("readFile content = %q", out)
	}

	if _, err := readFile(root, ""); err == nil {
		t.Error("readFile on a directory should error")
	}
	if _, err := readFile(root, "missing.txt"); err == nil {
		t.Error("readFile on a missing file should error")
	}
}

func TestReadFileTruncates(t *testing.T) {
	root := t.TempDir()
	big := strings.Repeat("x", maxReadBytes+1000)
	mustWrite(t, filepath.Join(root, "big.txt"), big)

	out, err := readFile(root, "big.txt")
	if err != nil {
		t.Fatalf("readFile: %v", err)
	}
	if !strings.Contains(out, "truncated") {
		t.Error("expected a truncation notice")
	}
	if len(out) > maxReadBytes+200 {
		t.Errorf("output not truncated: %d bytes", len(out))
	}
}

func TestSearchFiles(t *testing.T) {
	root := t.TempDir()
	mustWrite(t, filepath.Join(root, "deploy.yaml"), "x")
	mustWrite(t, filepath.Join(root, "sub", "deployment.go"), "x")
	mustWrite(t, filepath.Join(root, "sub", "readme.md"), "x")

	out, err := searchFiles(root, "", "deploy")
	if err != nil {
		t.Fatalf("searchFiles: %v", err)
	}
	if !strings.Contains(out, "deploy.yaml") || !strings.Contains(out, filepath.Join("sub", "deployment.go")) {
		t.Fatalf("search missing matches: %q", out)
	}
	if strings.Contains(out, "readme.md") {
		t.Errorf("search returned a non-match: %q", out)
	}

	if out, _ := searchFiles(root, "", "nonexistent"); !strings.Contains(out, "no files") {
		t.Errorf("expected no-match message, got %q", out)
	}
	if _, err := searchFiles(root, "", ""); err == nil {
		t.Error("empty query should error")
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
