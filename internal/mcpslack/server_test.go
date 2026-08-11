package mcpslack

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestLooksLikeChannelID(t *testing.T) {
	for _, id := range []string{"C12345", "G0ABCDE", "D99999"} {
		if !looksLikeChannelID(id) {
			t.Errorf("%q should look like a channel ID", id)
		}
	}
	for _, name := range []string{"general", "C", "team-updates", "#general", "lower123"} {
		if looksLikeChannelID(name) {
			t.Errorf("%q should NOT look like a channel ID", name)
		}
	}
}

func TestResolveMentions(t *testing.T) {
	nameOf := func(id string) string {
		if id == "U123" {
			return "alice"
		}
		return id
	}
	got := resolveMentions("hey <@U123> and <@U999>", nameOf)
	if got != "hey @alice and @U999" {
		t.Errorf("resolveMentions = %q", got)
	}
	// No mentions: text is unchanged.
	if got := resolveMentions("plain text", nameOf); got != "plain text" {
		t.Errorf("unchanged text = %q", got)
	}
}

func TestTSTime(t *testing.T) {
	tm, ok := tsTime("1700000000.000200")
	if !ok || tm.Unix() != 1700000000 {
		t.Errorf("tsTime = %v ok=%v, want unix 1700000000", tm.Unix(), ok)
	}
	if _, ok := tsTime("not-a-ts"); ok {
		t.Error("non-numeric ts should not parse")
	}
}

func TestFmtTS(t *testing.T) {
	// Unparseable ts falls back to the raw value.
	if got := fmtTS("garbage", time.UTC); got != "garbage" {
		t.Errorf("fallback = %q, want raw passthrough", got)
	}
	// A real ts renders in the given location.
	if got := fmtTS("1700000000.0", time.UTC); !strings.Contains(got, "2023-11-14") {
		t.Errorf("fmtTS = %q, want a 2023-11-14 date", got)
	}
}

func TestUserDisplayName(t *testing.T) {
	var u userInfoResp
	u.User.Profile.DisplayName = "ally"
	u.User.RealName = "Alice Smith"
	if got := u.displayName("U1"); got != "ally" {
		t.Errorf("prefers display_name, got %q", got)
	}

	var u2 userInfoResp
	u2.User.RealName = "Bob Jones"
	if got := u2.displayName("U2"); got != "Bob Jones" {
		t.Errorf("falls back to real_name, got %q", got)
	}

	// Nothing set -> the ID.
	var u3 userInfoResp
	if got := u3.displayName("U3"); got != "U3" {
		t.Errorf("falls back to ID, got %q", got)
	}
}

func TestChannelDisplayName(t *testing.T) {
	if got := (channel{Name: "general"}).displayName(); got != "general" {
		t.Errorf("named = %q", got)
	}
	if got := (channel{IsIM: true, User: "U42"}).displayName(); got != "dm:U42" {
		t.Errorf("im = %q", got)
	}
	if got := (channel{IsMPIM: true}).displayName(); got != "group-dm" {
		t.Errorf("mpim = %q", got)
	}
}

func TestFormatChannels(t *testing.T) {
	if formatChannels(nil) != "(no channels)" {
		t.Error("empty should render (no channels)")
	}
	out := formatChannels([]channel{
		{ID: "C1", Name: "general"},
		{ID: "C2", Name: "secret", IsPrivate: true},
		{ID: "D9", IsIM: true, User: "U7"},
	})
	for _, want := range []string{"C1\tgeneral\t(public)", "C2\tsecret\t(private)", "D9\tdm:U7\t(dm)"} {
		if !strings.Contains(out, want) {
			t.Errorf("formatChannels missing %q in:\n%s", want, out)
		}
	}
}

func TestFormatMessages(t *testing.T) {
	nameOf := func(id string) string {
		switch id {
		case "U1":
			return "alice"
		case "U2":
			return "bob"
		}
		return id
	}
	msgs := []message{
		{User: "U1", Text: "hi <@U2>", TS: "1700000000.0"},
		{User: "U2", Text: "", Subtype: "channel_join", TS: "1700000060.0"},
	}
	out := formatMessages(msgs, nameOf, time.UTC, "")
	if !strings.Contains(out, "alice: hi @bob") {
		t.Errorf("mention not resolved in:\n%s", out)
	}
	if !strings.Contains(out, "bob: (channel_join)") {
		t.Errorf("empty-body message should show its subtype:\n%s", out)
	}
	if formatMessages(nil, nameOf, time.UTC, "") != "(no messages)" {
		t.Error("empty should render (no messages)")
	}
}

// Whether the owner already replied is what the todo sweep turns on, so their
// own messages must be marked in the transcript rather than left for the model
// to infer by matching a display name against a configured handle.
func TestFormatMessagesMarksOwnMessages(t *testing.T) {
	nameOf := func(id string) string {
		if id == "U1" {
			return "Burc Sahinoglu"
		}
		return "alice"
	}
	msgs := []message{
		{User: "U2", Text: "can you review this?", TS: "1700000000.0"},
		{User: "U1", Text: "on it", TS: "1700000060.0"},
	}
	out := formatMessages(msgs, nameOf, time.UTC, "U1")
	if !strings.Contains(out, "Burc Sahinoglu (you): on it") {
		t.Errorf("own message not marked:\n%s", out)
	}
	if strings.Contains(out, "alice (you)") {
		t.Errorf("someone else's message wrongly marked:\n%s", out)
	}
	// Unknown identity (auth.test failed): render unmarked rather than fail.
	if out := formatMessages(msgs, nameOf, time.UTC, ""); strings.Contains(out, "(you)") {
		t.Errorf("nothing should be marked without an identity:\n%s", out)
	}
}

func TestFormatMatchesMarksOwnMessages(t *testing.T) {
	matches := []searchMatch{
		{User: "U1", Username: "burc", Text: "shipped it", TS: "1700000000.0"},
		{User: "U2", Username: "alice", Text: "thanks", TS: "1700000060.0"},
	}
	out := formatMatches(matches, time.UTC, "U1")
	if !strings.Contains(out, "burc (you): shipped it") {
		t.Errorf("own match not marked:\n%s", out)
	}
	if strings.Contains(out, "alice (you)") {
		t.Errorf("other match wrongly marked:\n%s", out)
	}
}

func TestReversed(t *testing.T) {
	// conversations.history is newest-first; reversed() gives oldest-first.
	in := []message{{TS: "3"}, {TS: "2"}, {TS: "1"}}
	out := reversed(in)
	if out[0].TS != "1" || out[2].TS != "3" {
		t.Errorf("reversed order = %v", []string{out[0].TS, out[1].TS, out[2].TS})
	}
	// Original slice is untouched.
	if in[0].TS != "3" {
		t.Error("reversed must not mutate its input")
	}
}

func TestMessageSender(t *testing.T) {
	if got := (message{Username: "webhook"}).sender(); got != "webhook" {
		t.Errorf("username sender = %q", got)
	}
	if got := (message{User: "U5"}).sender(); got != "U5" {
		t.Errorf("user sender = %q", got)
	}
	if got := (message{BotID: "B1"}).sender(); got != "bot:B1" {
		t.Errorf("bot sender = %q", got)
	}
	if got := (message{}).sender(); got != "system" {
		t.Errorf("empty sender = %q", got)
	}
}

func TestClampLimit(t *testing.T) {
	cases := []struct{ in, def, want int }{
		{0, 30, 30}, {-5, 30, 30}, {10, 30, 10}, {9999, 30, maxLimit},
	}
	for _, c := range cases {
		if got := clampLimit(c.in, c.def); got != c.want {
			t.Errorf("clampLimit(%d, %d) = %d, want %d", c.in, c.def, got, c.want)
		}
	}
}

func TestSlackResponseEnvelope(t *testing.T) {
	ok := slackResponse{OK: true}
	if !ok.isOK() {
		t.Error("OK envelope should report isOK")
	}
	bad := slackResponse{OK: false, Error: "not_allowed_token_type"}
	if bad.isOK() || bad.errMessage() != "not_allowed_token_type" {
		t.Errorf("bad envelope: isOK=%v msg=%q", bad.isOK(), bad.errMessage())
	}
	// Missing error string still yields a message.
	if (slackResponse{}).errMessage() == "" {
		t.Error("errMessage should never be empty")
	}
}

func TestLoadConfig(t *testing.T) {
	t.Setenv("AGENTBOX_SLACK_TOKEN", "")
	if _, ok := LoadConfig(); ok {
		t.Error("should be unconfigured without a token")
	}

	t.Setenv("AGENTBOX_SLACK_TOKEN", "xoxp-test")
	t.Setenv("AGENTBOX_SLACK_LOOKBACK_DAYS", "14")
	t.Setenv("AGENTBOX_TIMEZONE", "America/New_York")
	cfg, ok := LoadConfig()
	if !ok {
		t.Fatal("should be configured with a token")
	}
	if cfg.LookbackDays != 14 {
		t.Errorf("LookbackDays = %d, want 14", cfg.LookbackDays)
	}
	if cfg.Loc.String() != "America/New_York" {
		t.Errorf("Loc = %q", cfg.Loc.String())
	}

	// Whitespace-only token is treated as unset.
	t.Setenv("AGENTBOX_SLACK_TOKEN", "   ")
	if _, ok := LoadConfig(); ok {
		t.Error("whitespace token should be unconfigured")
	}
}

func TestIsMissingScope(t *testing.T) {
	if !isMissingScope(fmt.Errorf("conversations.list: missing_scope")) {
		t.Error("should detect missing_scope")
	}
	if isMissingScope(fmt.Errorf("conversations.list: invalid_auth")) {
		t.Error("should not flag a different error")
	}
	if isMissingScope(nil) {
		t.Error("nil is not missing_scope")
	}
}

// Channel listing must ask for the caller's own conversations, not every
// channel in the workspace: conversations.list ignores membership and pages
// enough times in a large org to hit Slack's rate limit on a routine briefing.
func TestListsOnlyTheUsersConversations(t *testing.T) {
	var gotPath string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C1","name":"team-eng"}],"response_metadata":{"next_cursor":""}}`))
	}))
	defer srv.Close()

	s := newServer(Config{Token: "x", Loc: time.UTC})
	s.base = srv.URL + "/"

	if _, err := s.channels(context.Background(), "public_channel"); err != nil {
		t.Fatal(err)
	}
	if gotPath != "/users.conversations" {
		t.Errorf("called %q, want /users.conversations", gotPath)
	}
}

// A 429 must be absorbed by the connector. Handing it to the model makes it
// improvise sleep-and-retry with run_bash, spending tool-call rounds against the
// cap on something the transport can do itself.
func TestCallRetriesAfterRateLimit(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		if hits == 1 {
			w.Header().Set("Retry-After", "1")
			w.WriteHeader(http.StatusTooManyRequests)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C1","name":"general"}],"response_metadata":{"next_cursor":""}}`))
	}))
	defer srv.Close()

	s := newServer(Config{Token: "x", Loc: time.UTC})
	s.base = srv.URL + "/"

	chans, err := s.channels(context.Background(), "public_channel")
	if err != nil {
		t.Fatalf("a rate-limited call should be retried, got: %v", err)
	}
	if hits != 2 {
		t.Errorf("want 2 requests (429 then success), got %d", hits)
	}
	if len(chans) != 1 {
		t.Errorf("channels = %+v", chans)
	}
}

// Retries are bounded: a persistently rate-limited endpoint has to surface an
// error rather than sleep indefinitely inside a scheduled run.
func TestCallGivesUpAfterBoundedRetries(t *testing.T) {
	var hits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		w.Header().Set("Retry-After", "1")
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	s := newServer(Config{Token: "x", Loc: time.UTC})
	s.base = srv.URL + "/"

	_, err := s.channels(context.Background(), "public_channel")
	if err == nil {
		t.Fatal("persistent rate limiting should surface an error")
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("error should name the cause, got: %v", err)
	}
	if want := maxRateLimitRetries + 1; hits != want {
		t.Errorf("want %d attempts, got %d", want, hits)
	}
}

func TestRetryAfterHeader(t *testing.T) {
	if got := retryAfter("12"); got != 12*time.Second {
		t.Errorf("retryAfter(12) = %v", got)
	}
	// Missing or junk values fall back rather than retrying instantly.
	if got := retryAfter(""); got != defaultRetryAfter {
		t.Errorf("retryAfter(empty) = %v, want %v", got, defaultRetryAfter)
	}
	if got := retryAfter("soon"); got != defaultRetryAfter {
		t.Errorf("retryAfter(junk) = %v, want %v", got, defaultRetryAfter)
	}
	// An absurd value is clamped so one bad header can't park the run.
	if got := retryAfter("3600"); got != maxRetryAfter {
		t.Errorf("retryAfter(3600) = %v, want %v", got, maxRetryAfter)
	}
}

// The per-run channel cache must be keyed by the requested types. It used to
// hold a single list for any request, so a list_channels call for public+private
// satisfied a later DM lookup and read_channel on a DM failed with "no channel
// named ..." even though the DM existed.
func TestChannelCacheIsKeyedByTypes(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		if strings.Contains(r.URL.Query().Get("types"), "im") {
			_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"D1","is_im":true,"user":"U9"}],"response_metadata":{"next_cursor":""}}`))
			return
		}
		_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C1","name":"general"}],"response_metadata":{"next_cursor":""}}`))
	}))
	defer srv.Close()

	s := newServer(Config{Token: "x", Loc: time.UTC})
	s.base = srv.URL + "/"
	ctx := context.Background()

	if _, err := s.channels(ctx, "public_channel,private_channel"); err != nil {
		t.Fatal(err)
	}
	dms, err := s.channels(ctx, "im,mpim")
	if err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Fatalf("a different types request must not reuse the cache: %d API calls", calls)
	}
	if len(dms) != 1 || dms[0].ID != "D1" {
		t.Errorf("DM lookup got %+v, want the D1 IM", dms)
	}

	// The same request, however spelled, is served from cache.
	if _, err := s.channels(ctx, " mpim , im "); err != nil {
		t.Fatal(err)
	}
	if _, err := s.channels(ctx, "im,mpim"); err != nil {
		t.Fatal(err)
	}
	if calls != 2 {
		t.Errorf("equivalent types should hit the cache, got %d API calls", calls)
	}
}

func TestNormalizeTypes(t *testing.T) {
	for in, want := range map[string]string{
		"":                                "public_channel,private_channel",
		"   ":                             "public_channel,private_channel",
		"im,mpim":                         "im,mpim",
		" mpim , im ":                     "im,mpim",
		"IM,MPIM":                         "im,mpim",
		"im,im":                           "im",
		"public_channel,private_channel":  "private_channel,public_channel",
		"private_channel, public_channel": "private_channel,public_channel",
	} {
		if got := normalizeTypes(in); got != want {
			t.Errorf("normalizeTypes(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestChannelsFallsBackOnMissingScope(t *testing.T) {
	var publicOnlyHits int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.Contains(r.URL.Query().Get("types"), "private_channel") {
			_, _ = w.Write([]byte(`{"ok":false,"error":"missing_scope"}`)) // token lacks groups:read
			return
		}
		publicOnlyHits++
		_, _ = w.Write([]byte(`{"ok":true,"channels":[{"id":"C1","name":"general"}],"response_metadata":{"next_cursor":""}}`))
	}))
	defer srv.Close()

	s := newServer(Config{Token: "x", Loc: time.UTC})
	s.base = srv.URL + "/"

	chans, err := s.channels(context.Background(), "public_channel,private_channel")
	if err != nil {
		t.Fatalf("channels should fall back, got error: %v", err)
	}
	if publicOnlyHits == 0 {
		t.Error("expected a retry requesting public channels only")
	}
	if len(chans) != 1 || chans[0].Name != "general" {
		t.Errorf("fallback channels = %+v, want [general]", chans)
	}
}
