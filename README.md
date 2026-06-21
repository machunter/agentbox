# agentbox

An autonomous AI agent, written in Go, that runs inside a Docker container.

Give it a task. It loops with Claude — reasoning and running `bash` commands in
its sandboxed container — until the task is done, then prints a short summary.
It remembers past sessions in a local vector store, so context carries across
runs without anything leaving the machine.

> **New here?** The [User Manual](MANUAL.md) is a step-by-step guide to
> installing agentbox and turning on each capability. This README is a shorter
> overview; [PRD.md](PRD.md) covers the design rationale.

```
┌──────────┐  task   ┌──────────────────────────────────────────────┐
│   you    │ ──────► │  agentbox (Google ADK)                         │
└──────────┘         │                                                │
                     │   ┌──────► Claude (Opus 4.8) ──────┐  think     │
                     │   │                                 ▼           │
                     │   │   run_bash ◄──── act      load/recall       │
                     │   │      │                       memory         │
                     │   └──────┘                          │           │
                     └────────────────────────────────────┼───────────┘
                                                           ▼
                                          ┌────────────────────────────┐
                                          │ local memory (in-process)   │
                                          │ chromem-go + Ollama embeds   │
                                          └────────────────────────────┘
```

## How it works

Built on [Google's Agent Development Kit (ADK) for Go](https://google.github.io/adk-docs/get-started/go/),
with Claude as the model via the [`adk-anthropic-go`](https://github.com/Alcova-AI/adk-anthropic-go)
adapter.

- **`main.go`** — reads the task (CLI arg or stdin), checks config, runs the agent.
- **`internal/agent`** — builds the ADK agent (Claude + tools) and drives the
  runner's event stream until the agent stops calling tools (or hits the turn
  cap). Persists the session to memory when the run ends.
- **`internal/tools`** — the agent's hands-on capability: `run_bash`, which runs
  a shell command in the container and returns its output.
- **`internal/mcpfs`** — a small filesystem MCP server (`list_directory`,
  `read_file`, `search_files`), jailed to the workspace. agentbox launches it as
  a subprocess of *itself* (the hidden `mcp-fs` subcommand) and connects via
  ADK's `mcptoolset` — the pattern for adding external connectors. It gives the
  model cleaner, read-only file primitives alongside `run_bash`.
- **`internal/mcpmail`** — a read-only email MCP server over IMAP
  (`list_new_emails`, `list_recent_emails`, `search_emails`, `read_email`,
  `list_mailboxes`), launched the same way. `list_new_emails` is incremental —
  a persisted per-mailbox UID watermark means briefings only see genuinely new
  mail. Mailbox aliases (`Sent`/`Drafts`/`Trash`/`Junk`/`Archive`)
  resolve to the provider's real folder via SPECIAL-USE. Enabled only when IMAP
  credentials are configured; otherwise silently skipped.
- **`internal/mcpcal`** — a read-only calendar MCP server over iCal (ICS) feeds
  (`list_upcoming_events`, `events_on_day`, `search_events`), expanding recurring
  events within the query window. Enabled only when feed URLs are configured.
- **`internal/mcpnotes`** — a local notes/todo MCP server (`add_todo`,
  `list_todos`, `complete_todo`, `add_note`, `search_notes`) over plain markdown
  (`todos.md` + `inbox.md`). Always on; the agent files and manages todos/notes
  for you, and they stay human-editable.
- **`internal/memory`** — a local implementation of ADK's `memory.Service`: it
  embeds session content and stores it in an embedded [chromem-go](https://github.com/philippgille/chromem-go)
  vector database, then retrieves it by semantic similarity. The agent gets
  relevant memories injected automatically and can also search them on demand.

The model is `claude-opus-4-8` with adaptive thinking. A turn cap (`maxTurns`)
and a per-command timeout keep a run bounded.

## Choosing a model

agentbox works with multiple LLM providers behind one interface. Pick the model
with `AGENTBOX_MODEL` and set the matching API key:

| `AGENTBOX_MODEL` | Provider | Key |
|---|---|---|
| `claude-opus-4-8` (default), `claude-sonnet-4-5`, … | Claude (Anthropic) | `ANTHROPIC_API_KEY` |
| `gemini-2.5-pro`, `gemini-2.5-flash`, … | Gemini (Google) | `GEMINI_API_KEY` or `GOOGLE_API_KEY` |

The provider is inferred from the name (`gemini*` → Gemini, otherwise Claude).
Everything else — memory, tools, connectors, vision capture — works the same
regardless of model.

## Memory & privacy

Embeddings are produced locally by [Ollama](https://ollama.com) (default model:
`nomic-embed-text`), and the vector store is in-process — so **your memory never
leaves the machine**. Memory is namespaced (`AGENTBOX_NAMESPACE`) so separate
deployments — say, a personal one and a work one — can't read each other's
memories.

Memory is optional and degrades gracefully: at startup the agent probes the
embedder, and if Ollama isn't reachable it logs a notice and runs without
memory rather than failing.

> **One honest caveat:** local embeddings keep your *memory index* on the box,
> but the **LLM runs in the cloud** (Claude via Anthropic, or Gemini via Google)
> — so the content the agent reasons over is still sent to the provider at
> inference time. "Local" applies to memory, not inference.

## Prerequisites

- An API key for your chosen model: Anthropic (Claude, default) or Google (Gemini)
- Go 1.25+ (to run locally) and/or Docker + Docker Compose (to run the stack)
- For memory: a local Ollama (optional locally; bundled in the compose stack)

## Run locally

```sh
cp .env.example .env        # then put your key in .env
export ANTHROPIC_API_KEY=sk-ant-...

make run TASK="list the files here and summarize what this project is"
# or
go build -o agentbox . && ./agentbox "create hello.txt with the current date"
# or pipe the task
echo "what's my go version?" | ./agentbox
```

To enable memory locally, run Ollama and pull the embedding model:

```sh
ollama pull nomic-embed-text   # agentbox finds Ollama at localhost:11434 by default
```

Without Ollama, agentbox still runs — it just prints a `[memory: disabled …]`
notice and skips long-term memory.

### Self-built tool library

Alongside semantic memory, the general agent has a **persistent, executable tool
library** at `tools/` (mounted at `/data/tools`, on the container `PATH`). Its
`INDEX.md` is injected into the agent's context each run. The agent is instructed
to reuse an existing tool before writing new code, and to save reusable scripts
back to the library — so over time it re-derives less and increasingly just
orchestrates tools it already built. Python 3 is the one sanctioned scripting
language (baked into the image), so the agent doesn't probe across languages. The
capture agent is deliberately excluded (notes-only, no shell). How well the
library grows depends on the model following the save/reuse protocol — stronger
models curate it far more reliably than a small flash model.

## Run the stack (Docker Compose)

This brings up agentbox plus a local Ollama, pulls the embedding model, and runs
the agent — memory and the model cache persist in named volumes:

```sh
export ANTHROPIC_API_KEY=sk-ant-...   # or put it in .env (compose reads it)
make compose-run TASK="summarize the files in /workspace"
# stop the stack:
make compose-down
```

The compose stack mounts the current directory as `/workspace` so the agent can
act on real files, and runs the agent as an unprivileged user.

## Giving the agent access to your files

Running in a container, the agent sees **only what you mount** — by default the
directory you launch from (at `/workspace`) and its own memory store. The rest of
your machine is invisible. That isolation is deliberate: it bounds what a
model-directed `run_bash` can touch. You grant more access explicitly.

- **Point the workspace elsewhere:** `AGENTBOX_WORKSPACE=~/Documents make compose-run TASK="..."`
- **Add directories for one run:** mount extra `host:container` pairs via `MOUNTS`
  (mount them *under* `/workspace` so the structured file tools, which are jailed
  there, can see them too):

  ```sh
  make compose-run MOUNTS="$HOME/Notes:/workspace/notes" \
    TASK="summarize my notes in /workspace/notes"
  ```

- **Add directories permanently:** copy `docker-compose.override.yml.example` to
  `docker-compose.override.yml` (gitignored) and list your mounts there; Compose
  merges it on every run. Use `:ro` for anything the agent shouldn't modify.

The agent refers to mounted dirs by their **container** path (`/workspace/notes`),
not the host path. On macOS, Docker Desktop maps your user's file ownership
transparently; on Linux, mind the unprivileged uid (10001) when mounting.
This pairs with `AGENTBOX_NAMESPACE`: a work deployment mounts work dirs, a
personal one mounts personal dirs, and neither can see the other's files or
memory.

## Todos & notes

agentbox keeps your todos and notes as plain markdown (`todos.md`, `inbox.md`) in
a notes directory (`AGENTBOX_NOTES_DIR`, default `notes/` under the workspace).
Just ask the agent in any run and it files them:

```sh
make compose-run TASK="add a todo to call the dentist, and note that I want to batch the briefings"
make compose-run TASK="what's on my todo list? mark the dentist one done."
```

Because they're plain files, you can also edit them directly — flip `- [ ]` to
`- [x]` in `todos.md` to mark something done — and a synced folder
(iCloud/Dropbox) mounted as the notes dir lets you capture from your phone.
Timestamps follow `AGENTBOX_TIMEZONE`.

To mark a todo done from the CLI without opening the file, use `agentbox done`
with a loose description — the model matches it to the right open todo (and
refuses if it's ambiguous):

```sh
docker compose exec agentbox-scheduler agentbox done "reply to yuval about the AI role"
```

### Capture from a photo

Snap a photo of a handwritten list (or anything with todos/notes) and drop it in
the **capture inbox** — Claude vision reads it and files the items. Point
`AGENTBOX_CAPTURE_HOST` at a synced folder (iCloud/Dropbox) so you can drop
photos from your phone, then:

```sh
make compose-run TASK=""        # or schedule it (below)
docker compose run --rm agentbox process-captures
```

Each image is read, its todos/notes filed via the notes tools, then **deleted**
(captures are transient, often photos of personal notes — only failures are kept,
in a `failed/` subfolder for inspection). Add a `command: process-captures` task
to `schedule.yaml` to do this automatically on a schedule. (Note: the image is
sent to Claude at inference time, like any other content.)

## Scheduler (long-lived mode)

Instead of one-shot tasks, agentbox can run as a long-lived process that executes
tasks on cron schedules — e.g. a daily morning briefing that reads your email and
calendar. On startup it also runs every task that fires at least daily once
immediately, so a (re)start delivers today's briefings/captures without waiting
for the next cron time (weekly/monthly tasks are skipped). Define tasks in a YAML
file:

```sh
mkdir -p config
cp schedule.example.yaml config/schedule.yaml   # then edit the schedules
docker compose up -d                            # starts Ollama + the scheduler
make compose-logs                               # follow the scheduler's output
```

The schedule lists **built-in tasks** — you only choose *when* each runs, not
*what* it does. The prompts live in the binary, so the file is just names + cron
times (no prompt-writing required):

```yaml
tasks:
  - name: daily-briefing      # built-in: email + calendar + todos, adapts to time of day
    schedule: "0 8,13,18 * * *"
  - name: process-captures    # built-in: file todos/notes from dropped photos
    schedule: "50 7,12,17 * * *"
  - name: weekly-review
    schedule: "0 17 * * 5"
```

Built-in names: `daily-briefing`, `weekly-review`, `process-captures`. Power
users can override any task with a free-form `prompt:` (or add a new prompt-only
task). On startup, tasks that run at least daily fire once immediately so a
(re)start delivers today's briefing without waiting (weekly/monthly are skipped).

The schedule is mounted as the `config/` **directory** (not a single file): on
macOS a single-file bind mount can trip a VirtioFS bug ("resource deadlock
avoided") from macOS extended attributes; a directory mount avoids it.

`schedule` is standard 5-field cron (`0 8 * * *`) or a descriptor (`@daily`).
Each run is an independent agent run that shares the persistent memory store. Set
`AGENTBOX_TIMEZONE` so schedules fire in your local time.

Test a task without waiting for its time:

```sh
make compose-run-task NAME=daily-briefing
```

Locally (no Docker): `AGENTBOX_SCHEDULE=schedule.yaml agentbox serve` (or
`agentbox run-task <name>`).

### Daily output

Each scheduled task's result is appended to a **dated markdown file** —
`journal/YYYY-MM-DD.md`, one per day — under a timestamped heading (prompt tasks
record the assistant's prose answer; command tasks record their output). It's the
assistant's delivery channel without email/push: read today's file to catch up on
the morning briefing, what it captured, and so on. Location is
`AGENTBOX_JOURNAL_DIR` (mounted from `AGENTBOX_JOURNAL_HOST` in the stack).

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `AGENTBOX_MODEL` | `claude-opus-4-8` | Model to use (`claude-*` or `gemini-*`). |
| `ANTHROPIC_API_KEY` | — | Claude API key (required for `claude-*` models). |
| `GEMINI_API_KEY` / `GOOGLE_API_KEY` | — | Google API key (required for `gemini-*` models). |
| `AGENTBOX_NAMESPACE` | `default` | Isolates memory per deployment (e.g. `personal`, `work`). |
| `AGENTBOX_MEMORY_DIR` | `~/.agentbox/memory` | Where the vector store persists. |
| `AGENTBOX_OLLAMA_URL` | Ollama's default | Embedder base URL (compose sets the service URL). |
| `AGENTBOX_EMBED_MODEL` | `nomic-embed-text` | Local embedding model. |
| `AGENTBOX_WORKSPACE` | `.` | Host dir mounted at `/workspace` (compose only). |
| `AGENTBOX_TOOLS_HOST` | `./tools` | Host dir for the agent's persistent, self-built tool library (compose only). |
| `AGENTBOX_IMAP_HOST` | — | IMAP server; set (with user/pass) to enable email tools. |
| `AGENTBOX_IMAP_PORT` | `993` | IMAP port (implicit TLS). |
| `AGENTBOX_IMAP_USER` | — | IMAP username. |
| `AGENTBOX_IMAP_PASS` | — | IMAP password (use an app password). |
| `AGENTBOX_EMAIL_SINCE_DAYS` | `0` (no limit) | Minimum lookback window (days) for the email tools; a per-call `since_days` can widen it but not narrow below this. |
| `AGENTBOX_ICS_URLS` | — | Calendar ICS feed URLs (comma-separated); enables calendar tools. |
| `AGENTBOX_CAL_TIMEOUT` | `60` | Seconds to fetch an ICS feed; raise for large feeds (tens of MB). |
| `AGENTBOX_CAL_CACHE_TTL` | `900` | Seconds to reuse a cached ICS feed before revalidating (conditional GET); `0` = always revalidate. |
| `AGENTBOX_TIMEZONE` | `UTC` | Timezone for day boundaries, event times, and cron schedules. |
| `AGENTBOX_SCHEDULE` | — | Path to the schedule YAML (required for `serve` / `run-task`). |
| `AGENTBOX_MAX_TOOL_CALLS` | `50` | Max tool-call rounds before a run is stopped (then it's asked to summarize). Raise for busy work mailboxes. |
| `AGENTBOX_DEBUG` | off | Set to `1` for verbose debug logging to stderr. |
| `AGENTBOX_NOTES_DIR` | `notes/` (under workspace) | Where todos.md / inbox.md live. |
| `AGENTBOX_CAPTURE_DIR` | `captures/` (under workspace) | Capture inbox the agent reads photos from. |
| `AGENTBOX_CAPTURE_HOST` | `./captures` | Host folder mounted as the capture inbox (point at a synced folder). |
| `AGENTBOX_JOURNAL_DIR` / `AGENTBOX_JOURNAL_HOST` | `journal/` | Daily-output markdown files (one per day) / host mount. |

### Email (read-only)

Set `AGENTBOX_IMAP_HOST`, `AGENTBOX_IMAP_USER`, and `AGENTBOX_IMAP_PASS` (e.g. in
`.env`) to give the agent read-only email tools: `list_mailboxes`,
`list_recent_emails`, `search_emails`, and `read_email`. Mailbox aliases
(`Sent`/`Drafts`/`Trash`/`Junk`/`Archive`) resolve to the provider's real folder
automatically, so the agent can scan sent mail — e.g. to confirm a reply went out
and close the matching todo. Use a provider **app password**, not your main
password. Sending is intentionally not supported yet — it will come as a
separate, confirmation-gated capability.

By default the email tools are count-bounded (most recent N messages). Set
`AGENTBOX_EMAIL_SINCE_DAYS` to also bound them by time (e.g. `7` = at least the
last week). This is a **minimum**: the agent can widen the window per call with a
`since_days` argument, but can't narrow it below your configured value — so if
you set `14`, every email scan covers at least 14 days regardless of what the
agent requests.

For recurring briefings, `list_new_emails` is **incremental**: it returns only
messages with a UID higher than the last one it processed for that mailbox, then
advances a persisted watermark (in `AGENTBOX_MAIL_STATE_DIR`, defaulting to the
memory volume). So the same email is never re-examined across the day's
briefings, which avoids duplicate todos. The watermark is per-mailbox and
UIDVALIDITY-aware (it re-baselines if the server resets UIDs).

Email stays subject to the same privacy
caveat as everything else: message content the agent reasons over is sent to
Anthropic at inference time.

### Calendar (read-only)

Set `AGENTBOX_ICS_URLS` to one or more iCal (ICS) feed URLs (comma-separated) to
give the agent read-only calendar tools: `list_upcoming_events`, `events_on_day`,
and `search_events` (recurring events are expanded). In Google Calendar, get the
URL from Settings → your calendar → Integrate calendar → **"Secret address in
iCal format"** — a private, read-only URL, so no OAuth or app password is needed.
Set `AGENTBOX_TIMEZONE` (e.g. `America/New_York`) so "today" and event times read
correctly. Test setup without the agent via `agentbox cal-check`.

## Debugging

Set `AGENTBOX_DEBUG=1` for a verbose trace on **stderr** (stdout stays clean for
the agent's answer). It logs the selected model/provider, every **tool call and
its result** (otherwise invisible), the agent's thinking, memory searches and
their hit counts, and capture decisions (including files skipped as unsupported
types). Each process reads it independently, so it covers the connector
subprocesses too.

```sh
AGENTBOX_DEBUG=1 docker compose run --rm agentbox "summarize my recent emails"
```

## Safety notes

The agent executes model-directed shell commands. That is the point — and the
reason it runs in a container as a non-root user. Treat the container as the
trust boundary: only mount directories you're comfortable letting the agent
read and modify, and review the task you give it.

## Extending it

Add a capability by defining a new ADK tool (an [`functiontool`](https://pkg.go.dev/google.golang.org/adk/tool/functiontool)
with a typed handler) in `internal/tools` and adding it to the agent's toolset
in `internal/agent/agent.go`. For external systems (email, calendar, files),
ADK's [`mcptoolset`](https://pkg.go.dev/google.golang.org/adk/tool/mcptoolset)
lets you wire in MCP servers. The loop machinery doesn't change.
