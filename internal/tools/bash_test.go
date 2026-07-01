package tools

import (
	"os"
	"strings"
	"testing"
)

func TestSafeEnvStripsSecrets(t *testing.T) {
	// Set representative secret and benign vars.
	secrets := map[string]string{
		"ANTHROPIC_API_KEY":             "sk-secret",
		"AGENTBOX_SLACK_TOKEN":          "xoxb-secret",
		"AGENTBOX_IMAP_PASS":            "hunter2",
		"AGENTBOX_GDRIVE_REFRESH_TOKEN": "1//refresh",
		"MY_DB_PASSWORD":                "pw",
	}
	benign := map[string]string{
		"AGENTBOX_TEST_HOME":    "/home/agent",
		"AGENTBOX_TEST_PATHISH": "/usr/bin",
	}
	for k, v := range secrets {
		t.Setenv(k, v)
	}
	for k, v := range benign {
		t.Setenv(k, v)
	}

	env := safeEnv()
	joined := strings.Join(env, "\n")

	for k := range secrets {
		if strings.Contains(joined, k+"=") {
			t.Errorf("safeEnv leaked secret var %q", k)
		}
	}
	for k := range benign {
		if !strings.Contains(joined, k+"=") {
			t.Errorf("safeEnv dropped benign var %q", k)
		}
	}
	// PATH should always survive so the agent can find its tools.
	if os.Getenv("PATH") != "" && !strings.Contains(joined, "PATH=") {
		t.Error("safeEnv dropped PATH")
	}
}

func TestCappedBufferTruncates(t *testing.T) {
	c := &cappedBuffer{max: 10}
	n, err := c.Write([]byte("0123456789ABCDEF"))
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 16 {
		t.Errorf("Write reported %d, want 16 (full length so the command isn't killed)", n)
	}
	got := c.String()
	if !strings.HasPrefix(got, "0123456789") {
		t.Errorf("kept prefix = %q, want first 10 bytes", got)
	}
	if !strings.Contains(got, "truncated") {
		t.Errorf("output = %q, want a truncation marker", got)
	}
}

func TestCappedBufferUnderLimit(t *testing.T) {
	c := &cappedBuffer{max: 100}
	c.Write([]byte("hello"))
	if got := c.String(); got != "hello" {
		t.Errorf("output = %q, want %q (no marker under the cap)", got, "hello")
	}
}
