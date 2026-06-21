package main

// Built-in scheduled-task prompts. Keeping them in the binary (not in
// schedule.yaml) means the schedule file is just task names + cron times —
// approachable for a non-technical user — while the prompts stay version-
// controlled and consistent. A task can still override these with its own
// `prompt:` in the schedule.

const dailyBriefingPrompt = "Give me a briefing tailored to the time of day. " +
	"First run `date` to get the current local time and decide whether this is the morning (start of day), midday, or evening (end of day) briefing. " +
	"Then: " +
	"(1) List today's calendar events. For the evening briefing, also preview tomorrow. " +
	"(2) Call list_new_emails to get mail that has arrived since the last briefing (it only returns messages not seen before, so you won't reprocess old ones), and pick out anything that needs my attention or a reply. For each email that needs an action from me, file a todo with add_todo — but first call list_todos and skip anything already captured, and don't create todos for newsletters, receipts, or FYI-only mail. " +
	"(3) Review my open todos with list_todos. For any todo about replying to or emailing someone, check whether I already handled it by searching my Sent mail: call search_emails with mailbox \"Sent\" (that name resolves to the real Sent folder automatically — do NOT call list_mailboxes) for that person/subject. If I already sent the reply, mark the todo done with complete_todo. " +
	"Only read the inbox and the Sent folder. Do not list, open, or search any other folders or labels. " +
	"Finish with a tight executive summary appropriate to the time of day (a few bullets, no preamble): what matters most now, my meetings, anything urgent in email, todos you created, and todos you closed. This summary is the only thing I'll read, so make it self-contained."

const weeklyReviewPrompt = "Review my calendar for the past week and draft a short summary of what I worked on, " +
	"based on events and any notes in /workspace. End with a tight executive summary I can skim."

// builtinPrompts maps built-in task names to their prompts, for the scheduler.
// A schedule.yaml task with just one of these names (and a schedule) runs the
// corresponding prompt.
func builtinPrompts() map[string]string {
	return map[string]string{
		"daily-briefing": dailyBriefingPrompt,
		"weekly-review":  weeklyReviewPrompt,
	}
}
