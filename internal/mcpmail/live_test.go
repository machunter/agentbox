package mcpmail

import (
	"testing"

	"github.com/emersion/go-imap/v2"
	"github.com/emersion/go-imap/v2/imapclient"
)

// TestLiveIMAP exercises the raw IMAP path (dial -> login -> select -> fetch)
// against a real server, bypassing MCP and the agent. It is skipped unless IMAP
// is configured in the environment. Diagnostic only.
func TestLiveIMAP(t *testing.T) {
	cfg, ok := LoadConfig()
	if !ok {
		t.Skip("IMAP not configured")
	}

	client, err := imapclient.DialTLS(cfg.Host+":"+cfg.Port, nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer client.Close()

	if err := client.Login(cfg.User, cfg.Pass).Wait(); err != nil {
		t.Fatalf("login: %v", err)
	}
	t.Log("login OK")

	sel, err := client.Select("INBOX", nil).Wait()
	if err != nil {
		t.Fatalf("select INBOX: %v", err)
	}
	t.Logf("INBOX has %d messages", sel.NumMessages)

	if sel.NumMessages == 0 {
		return
	}
	start := uint32(1)
	if sel.NumMessages > 3 {
		start = sel.NumMessages - 2
	}
	var seq imap.SeqSet
	seq.AddRange(start, sel.NumMessages)
	msgs, err := client.Fetch(seq, &imap.FetchOptions{Envelope: true, UID: true}).Collect()
	if err != nil {
		t.Fatalf("fetch: %v", err)
	}
	t.Logf("fetched %d envelopes; newest subject: %q", len(msgs), msgs[len(msgs)-1].Envelope.Subject)
}
