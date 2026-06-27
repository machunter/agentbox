package mcpslack

import (
	"context"
	"testing"
)

// TestLiveAuth exercises the real Slack Web API (auth.test) to confirm the token
// works, bypassing MCP and the agent. Skipped unless AGENTBOX_SLACK_TOKEN is
// set. Diagnostic only.
func TestLiveAuth(t *testing.T) {
	if !Configured() {
		t.Skip("AGENTBOX_SLACK_TOKEN not set")
	}
	out, err := CheckConnection(context.Background())
	if err != nil {
		t.Fatalf("auth.test: %v", err)
	}
	t.Logf("slack: %s", out)
}
