package agent

import (
	"bytes"
	"context"
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
