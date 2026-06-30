package mcpgdrive

import (
	"strings"
	"testing"
	"time"

	drive "google.golang.org/api/drive/v3"
)

func TestBuildQuery(t *testing.T) {
	if got := buildQuery("  "); got != "trashed = false" {
		t.Errorf("empty query = %q", got)
	}
	got := buildQuery("budget")
	if !strings.Contains(got, "trashed = false") ||
		!strings.Contains(got, "name contains 'budget'") ||
		!strings.Contains(got, "fullText contains 'budget'") {
		t.Errorf("query = %q", got)
	}
	// Single quotes are escaped so they can't break out of the literal.
	if got := buildQuery("rock'n'roll"); !strings.Contains(got, `rock\'n\'roll`) {
		t.Errorf("unescaped quote in %q", got)
	}
}

func TestExportMimeFor(t *testing.T) {
	cases := map[string]string{
		"application/vnd.google-apps.document":     "text/markdown",
		"application/vnd.google-apps.spreadsheet":  "text/csv",
		"application/vnd.google-apps.presentation": "text/plain",
	}
	for in, want := range cases {
		if !isGoogleDoc(in) {
			t.Errorf("%q should be a google doc type", in)
		}
		if got := exportMimeFor(in); got != want {
			t.Errorf("exportMimeFor(%q) = %q, want %q", in, got, want)
		}
	}
	if isGoogleDoc("application/pdf") {
		t.Error("pdf is not a native google doc")
	}
	if exportMimeFor("application/pdf") != "" {
		t.Error("non-google type should have no export mime")
	}
}

func TestIsTextual(t *testing.T) {
	for _, m := range []string{"text/plain", "text/markdown", "application/json", "application/xml"} {
		if !isTextual(m) {
			t.Errorf("%q should be textual", m)
		}
	}
	for _, m := range []string{"image/png", "application/pdf", "application/vnd.google-apps.document"} {
		if isTextual(m) {
			t.Errorf("%q should NOT be textual", m)
		}
	}
}

func TestFriendlyType(t *testing.T) {
	cases := map[string]string{
		"application/vnd.google-apps.document":    "doc",
		"application/vnd.google-apps.spreadsheet": "sheet",
		"application/vnd.google-apps.folder":      "folder",
		"application/pdf":                         "pdf",
		"image/png":                               "png",
		"weirdnoslash":                            "weirdnoslash",
	}
	for in, want := range cases {
		if got := friendlyType(in); got != want {
			t.Errorf("friendlyType(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFormatFiles(t *testing.T) {
	if formatFiles(nil, time.UTC) != "(no files)" {
		t.Error("empty should render (no files)")
	}
	files := []*drive.File{
		{Id: "abc123", Name: "Q3 Plan", MimeType: "application/vnd.google-apps.document", ModifiedTime: "2026-06-20T15:04:05Z"},
		{Id: "def456", Name: "notes.txt", MimeType: "text/plain", ModifiedTime: "not-a-time"},
	}
	out := formatFiles(files, time.UTC)
	if !strings.Contains(out, "abc123\tdoc\t2026-06-20 15:04\tQ3 Plan") {
		t.Errorf("doc line wrong:\n%s", out)
	}
	// Unparseable modified time falls back to the raw value rather than erroring.
	if !strings.Contains(out, "def456\tplain\tnot-a-time\tnotes.txt") {
		t.Errorf("fallback line wrong:\n%s", out)
	}
}

func TestClampLimit(t *testing.T) {
	cases := []struct{ in, want int }{{0, defaultLimit}, {-3, defaultLimit}, {10, 10}, {9999, maxLimit}}
	for _, c := range cases {
		if got := clampLimit(c.in); got != c.want {
			t.Errorf("clampLimit(%d) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestClip(t *testing.T) {
	if clip("short") != "short" {
		t.Error("short content should pass through")
	}
	big := strings.Repeat("x", maxDocBytes+500)
	out := clip(big)
	if len(out) <= maxDocBytes || !strings.Contains(out, "truncated") {
		t.Error("oversized content should be truncated with a marker")
	}
}

func TestLoadConfig(t *testing.T) {
	for _, k := range []string{"AGENTBOX_GDRIVE_CLIENT_ID", "AGENTBOX_GDRIVE_CLIENT_SECRET", "AGENTBOX_GDRIVE_REFRESH_TOKEN"} {
		t.Setenv(k, "")
	}
	if _, ok := LoadConfig(); ok {
		t.Error("should be unconfigured with no credentials")
	}

	t.Setenv("AGENTBOX_GDRIVE_CLIENT_ID", "cid")
	t.Setenv("AGENTBOX_GDRIVE_CLIENT_SECRET", "secret")
	t.Setenv("AGENTBOX_GDRIVE_REFRESH_TOKEN", "  refresh  ") // trimmed
	cfg, ok := LoadConfig()
	if !ok {
		t.Fatal("should be configured with all three set")
	}
	if cfg.RefreshToken != "refresh" {
		t.Errorf("refresh token not trimmed: %q", cfg.RefreshToken)
	}
	if cfg.oauthConfig().Scopes[0] != drive.DriveReadonlyScope {
		t.Errorf("expected drive.readonly scope, got %v", cfg.oauthConfig().Scopes)
	}
}
