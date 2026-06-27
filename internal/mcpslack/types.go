package mcpslack

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// --- Slack Web API response shapes ---

// apiResponse is the common envelope every Slack method returns. The typed
// responses embed slackResponse to satisfy it, so call() can check ok/error
// generically.
type apiResponse interface {
	isOK() bool
	errMessage() string
}

type slackResponse struct {
	OK               bool   `json:"ok"`
	Error            string `json:"error"`
	ResponseMetadata struct {
		NextCursor string `json:"next_cursor"`
	} `json:"response_metadata"`
}

func (r slackResponse) isOK() bool { return r.OK }

func (r slackResponse) errMessage() string {
	if r.Error != "" {
		return r.Error
	}
	return "request not ok"
}

type authTestResp struct {
	slackResponse
	User string `json:"user"`
	Team string `json:"team"`
	URL  string `json:"url"`
}

type channel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	IsPrivate bool   `json:"is_private"`
	IsIM      bool   `json:"is_im"`
	IsMPIM    bool   `json:"is_mpim"`
	User      string `json:"user"` // the other party, for IMs
}

// displayName renders a channel's human name, synthesizing one for DMs/groups
// that carry no name field.
func (c channel) displayName() string {
	switch {
	case c.Name != "":
		return c.Name
	case c.IsIM:
		return "dm:" + c.User
	case c.IsMPIM:
		return "group-dm"
	default:
		return c.ID
	}
}

type conversationsListResp struct {
	slackResponse
	Channels []channel `json:"channels"`
}

type message struct {
	Type     string `json:"type"`
	Subtype  string `json:"subtype"`
	User     string `json:"user"`
	BotID    string `json:"bot_id"`
	Username string `json:"username"`
	Text     string `json:"text"`
	TS       string `json:"ts"`
}

// sender returns the best available sender label for a message before user-ID
// resolution: an explicit username, or "bot" for bot posts, else the user ID.
func (m message) sender() string {
	if m.Username != "" {
		return m.Username
	}
	if m.User != "" {
		return m.User
	}
	if m.BotID != "" {
		return "bot:" + m.BotID
	}
	return "system"
}

type historyResp struct {
	slackResponse
	Messages []message `json:"messages"`
}

type searchMatch struct {
	Text     string `json:"text"`
	User     string `json:"user"`
	Username string `json:"username"`
	TS       string `json:"ts"`
	Channel  struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"channel"`
	Permalink string `json:"permalink"`
}

type searchResp struct {
	slackResponse
	Messages struct {
		Matches []searchMatch `json:"matches"`
	} `json:"messages"`
}

type userInfoResp struct {
	slackResponse
	User struct {
		Name     string `json:"name"`
		RealName string `json:"real_name"`
		Profile  struct {
			DisplayName string `json:"display_name"`
			RealName    string `json:"real_name"`
		} `json:"profile"`
	} `json:"user"`
}

// displayName picks the friendliest available name, falling back to the given ID.
func (u userInfoResp) displayName(id string) string {
	for _, c := range []string{u.User.Profile.DisplayName, u.User.Profile.RealName, u.User.RealName, u.User.Name} {
		if strings.TrimSpace(c) != "" {
			return c
		}
	}
	return id
}

// --- pure helpers (independently testable) ---

var channelIDRe = regexp.MustCompile(`^[CGD][A-Z0-9]{2,}$`)

// looksLikeChannelID reports whether ref is already a Slack conversation ID
// (channels/groups/DMs start with C/G/D), so we skip name resolution.
func looksLikeChannelID(ref string) bool {
	return channelIDRe.MatchString(ref)
}

var mentionRe = regexp.MustCompile(`<@([UW][A-Z0-9]+)>`)

// resolveMentions rewrites Slack user mentions (<@U123>) to @name using nameOf,
// so message text reads naturally instead of showing raw IDs.
func resolveMentions(text string, nameOf func(string) string) string {
	return mentionRe.ReplaceAllStringFunc(text, func(m string) string {
		id := mentionRe.FindStringSubmatch(m)[1]
		return "@" + nameOf(id)
	})
}

// tsTime parses a Slack timestamp ("1700000000.000200") into a time.Time. The
// second return is false when it can't be parsed.
func tsTime(ts string) (time.Time, bool) {
	sec := ts
	if i := strings.IndexByte(ts, '.'); i >= 0 {
		sec = ts[:i]
	}
	n, err := strconv.ParseInt(sec, 10, 64)
	if err != nil {
		return time.Time{}, false
	}
	return time.Unix(n, 0), true
}

// fmtTS renders a Slack ts in loc as "Mon 2006-01-02 15:04", or the raw ts if
// unparseable.
func fmtTS(ts string, loc *time.Location) string {
	if t, ok := tsTime(ts); ok {
		return t.In(loc).Format("Mon 2006-01-02 15:04")
	}
	return ts
}

func formatChannels(chans []channel) string {
	if len(chans) == 0 {
		return "(no channels)"
	}
	var b strings.Builder
	for _, c := range chans {
		kind := "public"
		switch {
		case c.IsIM:
			kind = "dm"
		case c.IsMPIM:
			kind = "group-dm"
		case c.IsPrivate:
			kind = "private"
		}
		fmt.Fprintf(&b, "%s\t%s\t(%s)\n", c.ID, c.displayName(), kind)
	}
	return strings.TrimRight(b.String(), "\n")
}

// formatMessages renders a transcript (assumed chronological), one message per
// line, resolving sender IDs and in-text mentions via nameOf.
func formatMessages(msgs []message, nameOf func(string) string, loc *time.Location) string {
	if len(msgs) == 0 {
		return "(no messages)"
	}
	var b strings.Builder
	for _, m := range msgs {
		who := m.sender()
		if m.User != "" {
			who = nameOf(m.User)
		}
		text := resolveMentions(strings.TrimSpace(m.Text), nameOf)
		if text == "" {
			text = "(" + nonText(m) + ")"
		}
		fmt.Fprintf(&b, "[%s] %s: %s\n", fmtTS(m.TS, loc), who, text)
	}
	return strings.TrimRight(b.String(), "\n")
}

// nonText describes a message with no body text (joins, file shares, etc.).
func nonText(m message) string {
	if m.Subtype != "" {
		return m.Subtype
	}
	return "no text"
}

func formatMatches(matches []searchMatch, loc *time.Location) string {
	if len(matches) == 0 {
		return "(no matches)"
	}
	var b strings.Builder
	for _, m := range matches {
		who := m.Username
		if who == "" {
			who = m.User
		}
		fmt.Fprintf(&b, "[%s] #%s %s: %s\n", fmtTS(m.TS, loc), m.Channel.Name, who, strings.TrimSpace(m.Text))
	}
	return strings.TrimRight(b.String(), "\n")
}

// reversed returns msgs in reverse order. conversations.history returns newest
// first; reversing gives a natural oldest-first transcript.
func reversed(msgs []message) []message {
	out := make([]message, len(msgs))
	for i, m := range msgs {
		out[len(msgs)-1-i] = m
	}
	return out
}

// clampLimit bounds a requested limit: <=0 uses def, and the result never
// exceeds maxLimit.
func clampLimit(n, def int) int {
	if n <= 0 {
		n = def
	}
	if n > maxLimit {
		return maxLimit
	}
	return n
}
