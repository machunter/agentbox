package agent

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// When the local embedder is unreachable, memory must disable itself (return
// nil) and print a notice — the agent should still be able to run.
func TestInitMemoryDisabledWhenEmbedderUnreachable(t *testing.T) {
	var out bytes.Buffer
	cfg := config{
		namespace:  "test",
		memoryDir:  "",                       // in-memory chromem store; no disk
		ollamaURL:  "http://127.0.0.1:1/api", // nothing listens here -> connection refused
		embedModel: "nomic-embed-text",
	}

	mem := initMemory(context.Background(), cfg, &out)
	if mem != nil {
		t.Fatal("expected memory to be disabled when the embedder is unreachable")
	}
	if !strings.Contains(out.String(), "memory: disabled") {
		t.Fatalf("expected a disabled-memory notice, got %q", out.String())
	}
}

func TestConfigFromEnvDefaults(t *testing.T) {
	for _, k := range []string{"AGENTBOX_NAMESPACE", "AGENTBOX_MEMORY_DIR", "AGENTBOX_OLLAMA_URL", "AGENTBOX_EMBED_MODEL"} {
		t.Setenv(k, "")
	}
	cfg := configFromEnv()
	if cfg.namespace != "default" {
		t.Errorf("namespace default: got %q, want %q", cfg.namespace, "default")
	}
	if cfg.embedModel == "" {
		t.Error("embedModel default should be non-empty")
	}
	if cfg.memoryDir == "" {
		t.Error("memoryDir should be derived when unset")
	}
}

func TestConfigFromEnvOverrides(t *testing.T) {
	t.Setenv("AGENTBOX_NAMESPACE", "work")
	t.Setenv("AGENTBOX_MEMORY_DIR", "/tmp/agentbox-work")
	t.Setenv("AGENTBOX_EMBED_MODEL", "embeddinggemma")
	cfg := configFromEnv()
	if cfg.namespace != "work" {
		t.Errorf("namespace: got %q, want %q", cfg.namespace, "work")
	}
	if cfg.memoryDir != "/tmp/agentbox-work" {
		t.Errorf("memoryDir: got %q", cfg.memoryDir)
	}
	if cfg.embedModel != "embeddinggemma" {
		t.Errorf("embedModel: got %q", cfg.embedModel)
	}
}

func TestForCaptureOption(t *testing.T) {
	var o options
	if o.capture {
		t.Fatal("default options should not be capture mode")
	}
	ForCapture()(&o)
	if !o.capture {
		t.Error("ForCapture() should set capture mode")
	}
}

func TestToolsDirEnv(t *testing.T) {
	t.Setenv("AGENTBOX_TOOLS_DIR", "/data/tools")
	if got := ToolsDir(); got != "/data/tools" {
		t.Errorf("ToolsDir() = %q, want /data/tools", got)
	}
	t.Setenv("AGENTBOX_TOOLS_DIR", "")
	if got := ToolsDir(); !strings.HasSuffix(got, ".agentbox/tools") {
		t.Errorf("default ToolsDir() = %q, want …/.agentbox/tools", got)
	}
}

func TestToolsSectionInjectsIndex(t *testing.T) {
	dir := t.TempDir()
	// No index yet -> protocol present, marked empty.
	s := toolsSection(dir)
	if !strings.Contains(s, dir) || !strings.Contains(s, "empty — no tools saved yet") {
		t.Errorf("empty toolsSection missing protocol/empty marker:\n%s", s)
	}
	// With an index -> its contents are injected.
	if err := os.WriteFile(filepath.Join(dir, "INDEX.md"), []byte("- weekday.py — print the weekday — `weekday.py <date>`"), 0o644); err != nil {
		t.Fatal(err)
	}
	s = toolsSection(dir)
	if !strings.Contains(s, "weekday.py") {
		t.Errorf("toolsSection did not inject the index:\n%s", s)
	}
}
