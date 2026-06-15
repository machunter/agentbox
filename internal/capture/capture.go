// Package capture processes a "capture inbox" of images — e.g. photos of
// handwritten notes dropped into a synced folder from a phone. For each image
// it runs the agent with the picture and an extraction prompt; the agent reads
// the handwriting (Claude vision) and files todos/notes via its notes tools.
// Processed images are moved aside so they aren't handled twice.
package capture

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const extractPrompt = "This is a photo the user took to quickly capture todos and notes. " +
	"Read all the text in the image (it may be handwritten). For each actionable item, call add_todo. " +
	"For anything that's a note, idea, or reference rather than a task, call add_note. " +
	"Keep each item concise and faithful to what's written. If the image has no usable text, do nothing and say so."

// imageMIME maps a file extension to a MIME type the model accepts, or "" if the
// file isn't a supported image/document.
func imageMIME(name string) string {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".jpg", ".jpeg":
		return "image/jpeg"
	case ".png":
		return "image/png"
	case ".gif":
		return "image/gif"
	case ".webp":
		return "image/webp"
	case ".pdf":
		return "application/pdf"
	default:
		return ""
	}
}

// Agent is the subset of the agent the processor needs.
type Agent interface {
	RunWithImage(ctx context.Context, prompt string, image []byte, mimeType string) error
}

// Factory builds a fresh Agent for one capture, writing output to out.
type Factory func(ctx context.Context, out io.Writer) (Agent, error)

// Process handles every supported image directly under dir (not recursing into
// the processed/ and failed/ subfolders). Successfully handled files are moved
// to processed/; failures to failed/. It returns the count processed.
func Process(ctx context.Context, dir string, out io.Writer, factory Factory) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, fmt.Errorf("read capture dir: %w", err)
	}

	processed := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		mime := imageMIME(e.Name())
		if mime == "" {
			continue // skip non-image files
		}

		path := filepath.Join(dir, e.Name())
		fmt.Fprintf(out, "\ncapture: processing %s\n", e.Name())
		if err := processOne(ctx, path, mime, out, factory); err != nil {
			fmt.Fprintf(out, "capture: %s failed: %v\n", e.Name(), err)
			moveAside(dir, "failed", e.Name(), out)
			continue
		}
		moveAside(dir, "processed", e.Name(), out)
		processed++
	}
	return processed, nil
}

func processOne(ctx context.Context, path, mime string, out io.Writer, factory Factory) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read image: %w", err)
	}
	ag, err := factory(ctx, out)
	if err != nil {
		return fmt.Errorf("agent setup: %w", err)
	}
	return ag.RunWithImage(ctx, extractPrompt, data, mime)
}

// moveAside moves name into dir/sub, keeping the inbox clean and avoiding
// reprocessing. Best-effort: a failure here is logged, not fatal.
func moveAside(dir, sub, name string, out io.Writer) {
	destDir := filepath.Join(dir, sub)
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		fmt.Fprintf(out, "capture: could not create %s/: %v\n", sub, err)
		return
	}
	if err := os.Rename(filepath.Join(dir, name), filepath.Join(destDir, name)); err != nil {
		fmt.Fprintf(out, "capture: could not move %s to %s/: %v\n", name, sub, err)
	}
}
