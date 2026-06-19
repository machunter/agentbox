package capture

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
)

type fakeAgent struct {
	calls *int
	err   error
}

func (f fakeAgent) RunWithImage(_ context.Context, _ string, _ []byte, _ string) error {
	*f.calls++
	return f.err
}

func fakeFactory(calls *int, err error) Factory {
	return func(_ context.Context, _ io.Writer) (Agent, error) {
		return fakeAgent{calls, err}, nil
	}
}

func TestImageMIME(t *testing.T) {
	cases := map[string]string{
		"a.jpg": "image/jpeg", "b.JPEG": "image/jpeg", "c.png": "image/png",
		"d.gif": "image/gif", "e.webp": "image/webp", "f.pdf": "application/pdf",
		"g.txt": "", "h": "", "i.heic": "",
	}
	for name, want := range cases {
		if got := imageMIME(name); got != want {
			t.Errorf("imageMIME(%q) = %q, want %q", name, got, want)
		}
	}
}

func TestProcessDeletesAndSkips(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "note1.jpg"), "fake-jpeg-bytes")
	write(t, filepath.Join(dir, "note2.png"), "fake-png-bytes")
	write(t, filepath.Join(dir, "ignore.txt"), "not an image")

	calls := 0
	n, err := Process(context.Background(), dir, io.Discard, fakeFactory(&calls, nil))
	if err != nil {
		t.Fatalf("Process: %v", err)
	}
	if n != 2 || calls != 2 {
		t.Fatalf("processed=%d calls=%d, want 2/2", n, calls)
	}
	// Successfully filed images are deleted (not moved to processed/).
	for _, name := range []string{"note1.jpg", "note2.png"} {
		if _, err := os.Stat(filepath.Join(dir, name)); !os.IsNotExist(err) {
			t.Errorf("%s should have been deleted from the inbox", name)
		}
	}
	if _, err := os.Stat(filepath.Join(dir, "processed")); !os.IsNotExist(err) {
		t.Errorf("no processed/ folder should be created")
	}
	if _, err := os.Stat(filepath.Join(dir, "ignore.txt")); err != nil {
		t.Errorf("non-image should be left in place: %v", err)
	}
}

func TestProcessFailureMovesToFailed(t *testing.T) {
	dir := t.TempDir()
	write(t, filepath.Join(dir, "bad.jpg"), "x")

	calls := 0
	n, err := Process(context.Background(), dir, io.Discard, fakeFactory(&calls, errors.New("vision failed")))
	if err != nil {
		t.Fatalf("Process should not return a top-level error: %v", err)
	}
	if n != 0 {
		t.Errorf("processed=%d, want 0 on failure", n)
	}
	if _, err := os.Stat(filepath.Join(dir, "failed", "bad.jpg")); err != nil {
		t.Errorf("failed image not moved to failed/: %v", err)
	}
}

func write(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
