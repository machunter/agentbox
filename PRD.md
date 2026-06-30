# agentbox — Product Requirements (living document)

> **Reverse PRD.** Written after the fact to capture what agentbox is, why each
> choice was made, and where it's going. It is updated as the product evolves —
> treat it as the source of intent, not a frozen spec.

**Status key:** ✅ Shipped (validated live) · 🟡 Shipped (unit-tested, not exercised live) · 🔵 Planned · 🤔 Considering
**Last updated:** 2026-06-29 (read-only Google Drive connector — Drive API + OAuth, exports native Google Docs to Markdown); 2026-06-28 (scope down calendar RSVP — feed-dependent, Google secret-iCal omits it; reconcile RECURRENCE-ID overrides); 2026-06-25 (read-only Slack connector); 2026-06-24 (mailbox aliases + `list_mailboxes`; calendar RSVP status) · **Branch of record:** `main`

---

## 1. Vision

agentbox is the seed of a **personal + work life assistant** — an autonomous agent
that helps organize a person's life and work, not merely a coding-task runner.
Today it reasons with Claude, runs shell commands, reads local files and email,
and remembers across sessions. The trajectory is toward an always-available
assistant that acts across the everyday sources a person uses — local files,
email, calendar, and more.

It runs as a container the user deploys themselves — typically **one instance on
a personal machine and one on a work machine** — keeping each context's data and
memory isolated.

## 2. Problem & motivation

General assistants don't have durable, private access to the messy particulars of
one person's life: their files, their mail, their schedule, and the context built
up over time. agentbox aims to be that grounded, persistent assistant — under the
user's control, on their own hardware, scoped to what they explicitly grant.

## 3. Users & personas

- **Primary user:** the owner-operator (a technical individual) running agentbox
  on their own machines for personal and work life management.
- **Two deployments, one person:** a *personal* deployment and a *work*
  deployment, each mounting only that context's directories and keeping a
  separate memory namespace. Neither can see the other's files or memory.

## 4. Principles

1. **Local-first for data.** Memory and embeddings stay on the machine. The
   deliberate, disclosed exception is **LLM inference** (Claude via Anthropic, or
   Gemini via Google): content the agent reasons over is sent to the chosen
   provider at inference time. "Local" applies to memory, not inference.
2. **The container is the trust boundary.** The agent executes model-directed
   commands; it runs sandboxed, as a non-root user, seeing only what is mounted.
3. **Explicit access.** The agent reaches a directory or account only when the
   user grants it (a mount, or configured credentials).
4. **Graceful degradation.** Optional capabilities (memory, email) disable
   themselves with a notice when their dependency is absent, rather than failing
   the run.
5. **Confirm outward-facing actions.** Anything that leaves the box or changes the
   world (sending mail, etc.) must be human-confirmed. *(Design pending — see §8.)*
6. **Connectors are the extension model.** New capabilities are MCP servers wired
   via ADK's `mcptoolset`, not bespoke loop changes.

## 5. Capabilities

| Capability | Status | Notes |
|---|---|---|
| Agentic loop (perceive→think→act) on Google ADK Go | ✅ | `maxTurns` safety cap |
| Multiple model providers (Claude, Gemini) | ✅ | `AGENTBOX_MODEL` selects; provider inferred from name; one `model.LLM` interface |
| `run_bash` tool | ✅ | Shell in the container; 60s per-command timeout |
| Long-term memory (recall across runs) | ✅ | Auto-recall (`preloadmemorytool`) + on-demand (`loadmemorytool`); persisted after each run |
| Filesystem tools (`list_directory`, `read_file`, `search_files`) | ✅ | MCP server jailed to `/workspace` |
| Todos & notes (`add_todo`, `list_todos`, `complete_todo`, `add_note`, `search_notes`) | ✅ | Local markdown store; always on; validated live. `add_todo` deterministically skips near-duplicates (token-overlap guard in `internal/mcpnotes`), so the same item arriving from two sources (email + Slack) won't create a repeat. A filing rubric in the agent prompt does the judgment the code can't: skip spam/cold-outreach and below-the-owner's-role admin tasks, prefer a note when unsure (owner operates at CTO level) |
| Photo capture (vision) of todos/notes | ✅ | Drop a photo in the capture inbox; Claude vision reads it and files items, then the image is deleted (failures kept in `failed/`); schedulable. Validated live |
| Voice capture of todos/notes | 🔵 | Same inbox, audio → text needs a local Whisper model (no cloud STT); v2 |
| Multimodal agent input (images) | ✅ | `RunWithImage`; adapter sends inline image as a vision block |
| Multi-directory access | ✅ | `MOUNTS` / compose override; mount under `/workspace` |
| Email — read (`list_new_emails`, `list_recent_emails`, `search_emails`, `read_email`, `list_mailboxes`) | ✅ | IMAP, read-only; count-bounded, optional date window (`AGENTBOX_EMAIL_SINCE_DAYS` floor / `since_days`). `list_new_emails` is incremental: a persisted per-mailbox UID watermark (UIDVALIDITY-aware) means briefings only process genuinely new mail, no reprocessing/duplicate todos. Mailbox aliases (`Sent`/`Drafts`/`Trash`/`Junk`/`Archive`) resolve to the provider's real folder via RFC 6154 SPECIAL-USE, so the agent can scan sent mail to e.g. close a todo once a reply went out |
| Calendar — read (`list_upcoming_events`, `events_on_day`, `search_events`) | ✅ | ICS feeds, read-only, recurrence-expanded, all-day aware; validated live. Configurable fetch timeout; in-run + cross-run caching (conditional GET / 304) so large feeds (tens of MB) aren't re-downloaded each run. The full feed is filtered to the query window in-process — only matched events enter the model context. RECURRENCE-ID overrides are reconciled against their master series (matched by UID + occurrence instant), so a modified/declined occurrence isn't double-counted. RSVP support (via `AGENTBOX_CAL_EMAIL`, default `AGENTBOX_IMAP_USER`) is **best-effort and feed-dependent**: when the feed carries the user's `PARTSTAT`, declined events are dropped and unconfirmed ones flagged `not yet accepted`/`tentative`; **Google's secret-iCal export omits attendees entirely, so RSVP is unavailable there and all events (declined included) are shown** — `cal-check` says so explicitly. Reliable per-attendee RSVP from Google would require the Calendar API (OAuth), a deliberate non-goal for now |
| Slack — read (`list_channels`, `read_channel`, `read_thread`, `search_messages`) | 🟡 | Slack Web API, read-only; single configured token (`AGENTBOX_SLACK_TOKEN`), no OAuth — user token (`xoxp-`) for search, bot token (`xoxb-`) for the rest. Resolves user IDs and `<@mentions>` to names; channel lookup by name or ID; history bounded by `AGENTBOX_SLACK_LOOKBACK_DAYS` / per-call `since_days`. Unit-tested; `slack-check` diagnostic. Posting deferred (confirmation-gated, like email send) |
| Google Drive — read (`search_drive`, `read_drive_file`, `list_recent_files`) | 🟡 | Drive API, read-only (`drive.readonly`); OAuth with a locally-stored refresh token from the `gdrive-login` flow, refreshed silently per run (headless after one consent). Native Google Docs/Sheets/Slides exported to Markdown/CSV/text — the thing ICS/mounts can't do. Unit-tested; `gdrive-check` diagnostic. **Workspace Internal app = no verification/CASA; personal Gmail needs restricted-scope verification (rclone mount is the fallback there).** Chosen over a mounted Drive / community MCP to keep tokens in the trust boundary and reuse the auth layer for future Workspace surfaces |
| Long-lived / scheduled operation (`serve`, `run-task`) | ✅ | Cron scheduler runs YAML-configured tasks in `AGENTBOX_TIMEZONE`; on startup it runs every daily-or-more-frequent task once (catch-up); run path validated live via `run-task`, timed firing via robfig/cron |
| Daily output journal | ✅ | Each scheduled task's result appended to `journal/YYYY-MM-DD.md` |
| Email delivery of digests | ✅ | Optional SMTP sink (`AGENTBOX_SMTP_HOST`) emails each digest to the user, reusing IMAP credentials; self-addressed only (`AGENTBOX_DELIVER_EMAIL`, default = IMAP user) |
| Self-built tool library | 🟡 | Persistent `tools/` dir (on PATH) + injected `INDEX.md`; the general agent reuses past scripts and saves new ones, so it re-derives less and increasingly orchestrates. Excludes capture. Quality depends on the model following the save/reuse protocol |
| CLI todo management | ✅ | `agentbox todo`/`todos` (deterministic, no key), `agentbox done` (LLM-matched) |
| Menu UX | ✅ | `agentbox.sh` — a zero-dependency host bash menu wrapping `docker compose` (exec when the scheduler is up, else one-off); no Makefile/source needed |
| Build version stamping | ✅ | git commit + build date injected via `-ldflags` (Makefile/Dockerfile); `agentbox version` and a scheduler-startup banner confirm which build a container is running |
| Email — send | 🔵 | Gated by human confirmation; design pending (§8) |
| Local LLM inference | 🤔 | Would remove the inference caveat; large effort, out of scope for now |

## 6. Architecture (as built)

- **Language/runtime:** Go (1.25+), single binary, Docker.
- **Agent framework:** Google **ADK Go** (`google.golang.org/adk`) — runner +
  `llmagent`; tools and toolsets registered on the agent.
- **Model:** pluggable via `internal/llm` (`AGENTBOX_MODEL`, default Claude
  **Opus 4.8**). Claude goes through the community **`adk-anthropic-go`** adapter;
  **Gemini** uses ADK's native `model/gemini`. Both expose ADK's `model.LLM`, so
  the rest of the agent is provider-agnostic.
- **Memory:** `internal/memory` implements ADK's `memory.Service` over an embedded
  **chromem-go** vector store (in-process, persisted to disk). Embeddings from a
  local **Ollama** model (`nomic-embed-text`). Namespaced per deployment; searches
  scoped to app+user.
- **Connectors:** self-launched MCP servers — agentbox runs *itself* in a
  subcommand (`mcp-fs`, `mcp-mail`) and connects via `mcptoolset` over stdio. One
  binary, no extra runtimes.
  - `internal/mcpfs` — filesystem, jailed to a root dir.
  - `internal/mcpmail` — IMAP, read-only, fresh connection per call.
- **Packaging:** `docker-compose` stack — agentbox + Ollama (+ one-shot model
  pull); named volumes persist memory and the model cache.

## 7. Configuration

| Variable | Purpose |
|---|---|
| `ANTHROPIC_API_KEY` | Required. Claude API key. |
| `AGENTBOX_NAMESPACE` | Memory isolation per deployment (`personal`/`work`). |
| `AGENTBOX_TOOLS_DIR` / `AGENTBOX_TOOLS_HOST` | Persistent tool library the agent builds and reuses (container path / host mount). |
| `AGENTBOX_MEMORY_DIR` | Vector-store location. |
| `AGENTBOX_OLLAMA_URL`, `AGENTBOX_EMBED_MODEL` | Local embedder. |
| `AGENTBOX_WORKSPACE`, `MOUNTS` | Host directories the agent can access. |
| `AGENTBOX_IMAP_HOST/PORT/USER/PASS` | Read-only email (use an app password). |
| `AGENTBOX_EMAIL_SINCE_DAYS` | Minimum lookback window (days) for email; a per-call `since_days` can widen but not narrow it; 0 = count-based only. |
| `AGENTBOX_MAIL_STATE_DIR` | Where `list_new_emails` persists per-mailbox UID watermarks (default: the memory dir). |
| `AGENTBOX_CAL_TIMEOUT` | HTTP timeout (seconds) for fetching ICS feeds; raise for large feeds (default 60). |
| `AGENTBOX_CAL_CACHE_TTL` | Seconds to reuse a cached ICS feed before revalidating via conditional GET (default 900; 0 = always revalidate). |
| `AGENTBOX_MAX_TOOL_CALLS` | Tool-call rounds before a run stops and is asked to summarize (default 50). |
| `AGENTBOX_DEBUG` | Verbose debug logging to stderr (off by default). |
| `AGENTBOX_ICS_URLS`, `AGENTBOX_TIMEZONE` | Read-only calendar (ICS feeds) + day-boundary / cron timezone. |
| `AGENTBOX_SLACK_TOKEN`, `AGENTBOX_SLACK_LOOKBACK_DAYS` | Read-only Slack (Web API token) + default channel-history lookback window. |
| `AGENTBOX_GDRIVE_CLIENT_ID` / `_CLIENT_SECRET` / `_REFRESH_TOKEN` | Read-only Google Drive (OAuth client + refresh token from `gdrive-login`). |
| `AGENTBOX_CAL_EMAIL` | User's own calendar address(es); enables per-event RSVP status. Defaults to `AGENTBOX_IMAP_USER`. |
| `AGENTBOX_SCHEDULE` | Path to the schedule YAML (for `serve` / `run-task`). |
| `AGENTBOX_NOTES_DIR` | Where todos.md / inbox.md live (default `notes/` under the workspace). |
| `AGENTBOX_CAPTURE_DIR` / `AGENTBOX_CAPTURE_HOST` | Capture inbox the agent reads photos from / the host folder mounted as it. |
| `AGENTBOX_JOURNAL_DIR` / `AGENTBOX_JOURNAL_HOST` | Daily-output markdown files (one per day) / host mount. |

## 8. Open questions / decisions pending

- **Send-confirmation channel.** Human-in-the-loop confirmation (`mcptoolset`'s
  `RequireConfirmation`) needs an approval path, but the agent is a one-shot,
  non-interactive container today. Resolving this is a prerequisite for SMTP send
  and any other write action.
- **Scheduler result delivery.** *(Partly addressed: scheduled results are now
  written to a daily markdown journal — `journal/YYYY-MM-DD.md`.)* Push/email
  delivery is still open and pairs naturally with the email-send capability.
- **Per-run connector cost.** Each scheduled run builds a fresh agent (re-launches
  connector subprocesses, re-probes Ollama). Fine at daily cadence; revisit if
  schedules get frequent.
- **Per-context live validation.** Namespace isolation is unit-tested; not yet
  exercised across two live deployments.

## 9. Non-goals (for now)

- Local LLM inference (Claude inference stays remote).
- OAuth / provider-specific APIs (Gmail API, etc.) — IMAP/SMTP is the universal
  path chosen.
- A graphical UI; multi-user/tenant operation.
- Event-driven/reactive triggers (e.g. act on each new email). The scheduler is
  cron/time-based for now; reactive triggers are a later consideration.

## 10. Decision log

| Decision | Choice | Why |
|---|---|---|
| Agent framework | Google ADK Go | Official, multi-agent + MCP + OTel; avoids a hand-rolled loop as scope grows |
| Model adapter | `adk-anthropic-go` | Keeps Claude as the model on ADK; fallback wrapper if it lapses |
| Multiple providers | Selector behind ADK's `model.LLM` (`internal/llm`) | Gemini is native to ADK; provider inferred from model name; everything downstream is provider-agnostic |
| Vector store | Embedded chromem-go | In-process, single-container; no separate DB service |
| Embeddings | Local Ollama `nomic-embed-text` | Keeps memory fully local; rejected hosted Voyage on privacy |
| Email protocol | IMAP/SMTP (app password) | Universal across providers; no OAuth server; fits local ethos |
| Email v1 scope | Read-only | Sending is outward-facing; land read first, gate send later |
| Calendar access | Read-only ICS secret-URL feeds | Google deprecated app-password CalDAV (needs OAuth); ICS secret URL is read-only, no OAuth, universal — consistent with the email decision |
| Calendar before email-send | Reprioritized | A read-only connector ships value now without resolving the send-confirmation design first |
| Connector packaging | Self-launched MCP subprocess | One binary, no Node/second artifact; uniform pattern |
| Scheduling | In-process cron daemon (`serve`) | Self-contained, keeps memory warm, one container; over external cron + one-shot |
| Schedule config | YAML file (tasks: name/schedule/prompt) | Human-editable; mounted in; `run-task` for testing |
| Scheduled run isolation | Fresh agent + session per fire | Tasks don't bleed into each other; still share durable memory |
| Daily-output delivery | Dated markdown journal (no SMTP) | Readable, greppable, syncable; records the assistant's prose answer (not tool traces); SMTP/push later |
| Notes/todos store | Plain markdown in separate `todos/` (todos.md + done/) and `notes/` (inbox.md) dirs | Self-explanatory folders, human-editable + syncable; precise tools beat LLM hand-editing. Completing a todo (tool or hand-marked `- [x]`) moves it into a dated `todos/done/<date>.md` on the next todo op, so the active list stays readable |
| Phone capture path | Photo of notes (Claude vision) over email-to-self | Email felt impractical to the user; a snapped photo is more natural. Vision reuses the model we already have (no new service); deleting each image after its items are filed both prevents reprocessing and keeps no copies of personal notes (failures are kept in `failed/`) |
| Capture inbox processing | Image in the user message (not a tool result) | The adapter's verified image path is on input; per-image fresh agent run keeps captures isolated |
| Built-in task prompts | Prompts live in the binary; schedule.yaml is just names + cron | Editing prompt engineering in YAML is intimidating for a layman. A bare task name selects a built-in (`daily-briefing`/`weekly-review`/`process-captures`). Override per task with an editable `config/prompts/<name>.md` file (re-read each run) or an inline `prompt:` — power users get full control without touching the simple default |
| Capture agent is locked down | Notes-only toolset (no run_bash/files/email/calendar/memory) via `ForCapture()` | Capture only needs to read an image and file todos/notes; a shell let weak models wander (run agentbox recursively, probe the network, attempt installs). Removing the surface is more reliable than prompting against it |
