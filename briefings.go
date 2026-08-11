package main

import (
	"os"
	"path/filepath"
	"strings"

	"github.com/burcsahinoglu/agentbox/internal/capture"
	"github.com/burcsahinoglu/agentbox/internal/schedule"
)

// Built-in scheduled-task prompts. Keeping them in the binary (not in
// schedule.yaml) means the schedule file is just task names + cron times —
// approachable for a non-technical user — while the prompts stay version-
// controlled and consistent. A task can still override these with its own
// `prompt:` in the schedule.

const dailyBriefingPrompt = "Give me a briefing tailored to the time of day. " +
	"First run `date` to get the current local time and decide whether this is the morning (start of day), midday, or evening (end of day) briefing. " +
	"Then: " +
	"(1) List today's calendar events. For the evening briefing, also preview tomorrow. " +
	"(2) Call list_new_emails to get mail that has arrived since the last briefing (it only returns messages not seen before, so you won't reprocess old ones), and pick out anything that needs my attention or a reply. For each email that genuinely needs an action from me, file a todo with add_todo, setting source to \"email:<sender>#<uid>\" with the UID from the listing (the sweep needs it to tell whether I later deleted the mail); first call list_todos and skip anything already captured. " +
	"Be strict about what earns a todo. No newsletters, receipts, automated notifications, or FYI-only mail, and no cold outreach: sales and vendor pitches, recruiters, conference or webinar invitations, and unsolicited meeting or demo requests from people I don't already deal with. When a message asks for my time and the sender isn't someone I recognize, check whether I've ever written to them (search_emails with mailbox \"Sent\" for their address or name): no prior correspondence from me means it's cold outreach, so do not file a todo. Mention it in the summary if it looks genuinely worth my attention, otherwise drop it. " +
	"(3) Review my open todos with list_todos. Each todo records where it came from in its trailing comment (e.g. src:email:jary#4821 or src:slack:team-eng/1723456.789). Look for my reply at that origin, not somewhere else. " +
	"For src:email, first call email_exists with that UID. If it comes back gone, I deleted or filed the message, which means it no longer needs doing: complete_todo it and move on without searching for a reply. Only if it's still in the inbox, search my Sent mail: call search_emails with mailbox \"Sent\" (that name resolves to the real Sent folder automatically; do NOT call list_mailboxes) for that person/subject. Todos with no src recorded predate source tracking, so check Sent mail for those. " +
	"For src:slack, check Slack instead: read_thread when the source carries a thread timestamp, otherwise read_channel or search_messages on that channel. My own messages are marked \"(you)\" in the transcript, so a message from me after the request is the reply, and you should not have to guess from names. If a todo lists several sources, a reply at any one of them resolves it. Mark anything already handled done with complete_todo. " +
	"Only read the inbox and the Sent folder. Do not list, open, or search any other folders or labels. " +
	"(4) If Slack is configured, take a quick look for anything needing my attention: skim the channels most relevant to me (use list_channels to find them, then read_channel for the last day), and use search_messages for recent messages that mention me or ask me a direct question. File a todo for anything that needs a reply, setting source to \"slack:<channel>\" and appending /<thread-ts> when it's a thread, so a later sweep can read that thread directly. Keep it light: skip routine chatter and don't read whole histories. " +
	"Finish with a tight executive summary appropriate to the time of day (a few bullets, no preamble): what matters most now, my meetings, anything urgent in email or Slack, todos you created, and todos you closed. This summary is the only thing I'll read, so make it self-contained."

const weeklyReviewPrompt = "Review my calendar for the past week and draft a short summary of what I worked on, " +
	"based on events and any notes in /workspace. End with a tight executive summary I can skim."

// builtinPrompts maps built-in task names to their default prompts.
func builtinPrompts() map[string]string {
	return map[string]string{
		"daily-briefing": dailyBriefingPrompt,
		"weekly-review":  weeklyReviewPrompt,
	}
}

// readPromptOverride returns the contents of AGENTBOX_PROMPTS_DIR/<name>.md when
// present and non-empty. Re-read on each call, so edits take effect on the next
// run without a rebuild.
func readPromptOverride(name string) (string, bool) {
	dir := os.Getenv("AGENTBOX_PROMPTS_DIR")
	if dir == "" {
		return "", false
	}
	b, err := os.ReadFile(filepath.Join(dir, name+".md"))
	if err != nil {
		return "", false
	}
	if s := strings.TrimSpace(string(b)); s != "" {
		return s, true
	}
	return "", false
}

// promptResolver returns the prompt for a task name: a user override file takes
// precedence over the binary default. An override file can also define a prompt
// for a brand-new task name.
func promptResolver() schedule.PromptFunc {
	defaults := builtinPrompts()
	return func(name string) (string, bool) {
		if s, ok := readPromptOverride(name); ok {
			return s, true
		}
		def, ok := defaults[name]
		return def, ok
	}
}

// captureExtractPrompt is the image-extraction instruction for process-captures,
// overridable via config/prompts/process-captures.md like the prompt tasks.
func captureExtractPrompt() string {
	if s, ok := readPromptOverride("process-captures"); ok {
		return s
	}
	return capture.DefaultExtractPrompt
}
