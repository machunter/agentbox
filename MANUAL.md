# agentbox — User Manual

A practical, step-by-step guide to installing and using agentbox. agentbox is a
personal assistant you run yourself: it reasons with Claude, runs commands in a
sandboxed container, reads your files/email/calendar, manages your todos and
notes, remembers across sessions, and can run tasks on a schedule.

> **What stays private:** your memory and embeddings never leave your machine.
> The one exception is the language model — Claude runs via the Anthropic API,
> so whatever the agent reasons over (an email body, a file, a photo) is sent to
> Anthropic at the moment it's processed. "Local" applies to memory, not
> inference. See [Privacy](#privacy).

## Contents

1. [What you'll need](#1-what-youll-need)
2. [Install](#2-install)
3. [Your first task](#3-your-first-task)
4. [Turn on memory](#4-turn-on-memory)
5. [Give it your files](#5-give-it-your-files)
6. [Connect email](#6-connect-email-read-only)
7. [Connect your calendar](#7-connect-your-calendar-read-only)
8. [Connect Slack](#8-connect-slack-read-only)
9. [Todos & notes](#9-todos--notes)
10. [Capture from a photo](#10-capture-from-a-photo)
11. [Run it on a schedule](#11-run-it-on-a-schedule)
12. [Personal vs work](#12-personal-vs-work)
13. [Privacy](#privacy)
14. [Troubleshooting](#14-troubleshooting)
15. [Reference](#15-reference)

---

## 1. What you'll need

- **An API key for your model.** By default agentbox uses Claude — get an
  **Anthropic API key** with credits at
  [console.anthropic.com](https://console.anthropic.com) (add credits under
  **Plans & Billing**; a zero balance returns a "credit balance is too low"
  error). To use **Gemini** instead, get a **Google API key** and set
  `AGENTBOX_MODEL=gemini-2.5-pro` — see [Choosing a model](#choosing-a-model).
- **Docker Desktop** (recommended) — runs the whole stack, including a local
  Ollama for memory. *Or* **Go 1.25+** to build and run natively.
- Optional, per feature: an email **app password**, a calendar **ICS URL**.

## 2. Install

```sh
git clone <repo-url> agentbox
cd agentbox
cp .env.example .env          # then open .env and paste your key
```

Put your key in `.env`:

```
ANTHROPIC_API_KEY=sk-ant-...
```

`.env` is gitignored — it won't be committed. Docker Compose reads it
automatically.

**With Docker (recommended):** nothing else to build — the first run builds the
image. **Native:** `make build` produces an `agentbox` binary.

### Run from the published image (no source checkout)

If you'd rather not clone the repo, you can run the published Docker image on any
machine with just Docker. Grab the `deploy/` folder (or its three files) — see
[deploy/README.md](deploy/README.md):

```sh
cd deploy
cp .env.example .env        # set AGENTBOX_MODEL + your API key
docker compose pull
docker compose run --rm agentbox "summarize the files in /workspace"
```

That compose pulls `machunter/agentbox:latest` instead of building from source.
The rest of this manual applies the same way.

## 3. Your first task

```sh
make compose-run TASK="list the files here and summarize what this project is"
```

The first run downloads the Ollama image and the embedding model (a few
minutes, one time). You'll see the agent's tool calls (e.g. `› run_bash …`) and
a final summary. That's the one-shot mode — give it a task, it works, it stops.

Native equivalent (after `make build`):

```sh
export ANTHROPIC_API_KEY=sk-ant-...
./agentbox "list the files here and summarize what this project is"
```

## 3b. Choosing a model

agentbox supports Claude and Gemini behind one interface. Set `AGENTBOX_MODEL`
and the matching API key in `.env`:

```
# Claude (default) — needs ANTHROPIC_API_KEY
AGENTBOX_MODEL=claude-opus-4-8

# …or Gemini — needs GEMINI_API_KEY (or GOOGLE_API_KEY)
AGENTBOX_MODEL=gemini-2.5-pro
GEMINI_API_KEY=...
```

The provider is inferred from the name (`gemini*` → Gemini, otherwise Claude).
You only need the key for the model you pick. Everything else — memory, tools,
connectors, photo capture — works the same either way.

## 4. Turn on memory

Memory lets agentbox recall earlier sessions. With Docker it's **automatic** —
the stack runs a local Ollama and pulls the `nomic-embed-text` model on first
use. Try it:

```sh
make compose-run TASK="remember that my deploy key lives in vault path secret/agentbox"
make compose-run TASK="where does my deploy key live?"
```

Memory is **optional and self-disabling**: if the embedder isn't reachable you'll
see `[memory: disabled …]` and the agent runs without it. (Native users: run
`ollama pull nomic-embed-text` to enable it.)

## 5. Give it your files

In the container the agent only sees what you mount. By default that's the
directory you launch from, at `/workspace`. To give it other folders:

- **One run:** `make compose-run MOUNTS="$HOME/Notes:/workspace/notes" TASK="summarize /workspace/notes"`
- **Always:** copy `docker-compose.override.yml.example` to
  `docker-compose.override.yml` and list your folders there.

Mount folders **under `/workspace`** so the structured file tools see them, and
refer to them by their container path (`/workspace/notes`). Use `:ro` for
read-only.

## 6. Connect email (read-only)

agentbox can list, search, and read your mail over IMAP. Add to `.env`:

```
AGENTBOX_IMAP_HOST=imap.gmail.com
AGENTBOX_IMAP_USER=you@example.com
AGENTBOX_IMAP_PASS=your16charapppassword
```

**Use an app password, not your account password.** For Gmail:
[create one](https://myaccount.google.com/apppasswords) (requires 2-Step
Verification), and **paste it with no spaces** — Google shows it in four spaced
groups for readability, but the value must be spaceless. Make sure IMAP is
enabled in Gmail settings.

Test the connection without spending API credits:

```sh
docker compose run --rm --entrypoint agentbox agentbox mail-check
```

Then ask the agent: `make compose-run TASK="summarize my 5 most recent emails"`.

By default the email tools return the most recent N messages (count-based, no
date cutoff). To bound by time, set `AGENTBOX_EMAIL_SINCE_DAYS=7` (at least the
last week). This is a **minimum window**: the agent can widen it per request
("emails from the last 30 days") but can't go below your setting — so
`AGENTBOX_EMAIL_SINCE_DAYS=14` guarantees every scan covers at least 14 days,
even if a briefing only asks for "today's" mail.

For the briefings, the agent uses `list_new_emails`, which is **incremental**:
it returns only mail it hasn't processed before (tracked by a per-mailbox UID
watermark saved in `AGENTBOX_MAIL_STATE_DIR`, defaulting to the memory volume),
so the same message isn't turned into a todo twice across the day's briefings.
The first run after setup processes the recent backlog (bounded by the
since-days window), then each later run only sees genuinely new mail.

The agent can also scan folders other than the inbox. Mailbox aliases —
`Sent`, `Drafts`, `Trash`, `Junk`, `Archive` — resolve to your provider's actual
folder name (which varies: `Sent`, `Sent Items`, `[Gmail]/Sent Mail`, …) via the
IMAP SPECIAL-USE attribute, and `list_mailboxes` shows every folder with its
role. So you can ask things like *"did I already reply to Dana? if so close that
todo"* and the agent will check your Sent mail before acting.

## 7. Connect your calendar (read-only)

agentbox reads your calendar from an **iCal (ICS) feed URL** — no OAuth needed.

In Google Calendar: **Settings → [your calendar] → Integrate calendar → "Secret
address in iCal format."** Copy that URL into `.env`, and set your timezone so
"today" is correct:

```
AGENTBOX_ICS_URLS=https://calendar.google.com/calendar/ical/.../basic.ics
AGENTBOX_TIMEZONE=America/Los_Angeles
```

Multiple calendars: comma-separate the URLs. Test:

```sh
docker compose run --rm --entrypoint agentbox agentbox cal-check
```

Then: `make compose-run TASK="what's on my calendar this week?"`.

**Set your own address so RSVP status is read correctly.** An ICS feed lists
every event you're invited to, including ones you haven't answered. Set
`AGENTBOX_CAL_EMAIL=you@example.com` (it defaults to `AGENTBOX_IMAP_USER`) and the
agent reads *your* `PARTSTAT` on each event, flagging unconfirmed ones as `not yet
accepted`, `tentative`, or `DECLINED` — so it won't tell you you're attending a
meeting you never accepted. Comma-separate multiple addresses/aliases. Without an
address, events carry no RSVP info (prior behavior).

## 8. Connect Slack (read-only)

agentbox reads Slack through a single **token** — no OAuth flow. You create a
small Slack app, give it read permission, and paste its token into `.env`:

```
AGENTBOX_SLACK_TOKEN=xoxp-...
```

To get the token:

1. Go to **api.slack.com/apps → Create New App → From scratch**, pick your
   workspace.
2. Open **OAuth & Permissions** and add **User Token Scopes**:
   - `channels:read`, `groups:read` — list public and private channels
   - `channels:history`, `groups:history` — read channel messages and threads
   - `users:read` — resolve user IDs to names
   - `search:read` — `search_messages`
   - *(optional, for DMs)* `im:read`, `mpim:read`, `im:history`, `mpim:history`
3. Click **Install to Workspace** and authorize. (Adding scopes later requires a
   **reinstall** to take effect.)
4. Copy the **User OAuth Token** (`xoxp-…`) into `AGENTBOX_SLACK_TOKEN`.

A **user** token is recommended because `search_messages` only works on user
tokens; a bot token (`xoxb-…`) covers the other tools if you prefer. If the token
is missing a scope you'll see `missing_scope` — add it and reinstall. (Channel
listing degrades gracefully: with only `channels:read`, it lists public channels
and skips private ones rather than failing.) Test it without spending API credits:

```sh
docker compose run --rm --entrypoint agentbox agentbox slack-check
```

Then ask: `make compose-run TASK="what did I miss in #engineering today?"`. The
agent finds the channel with `list_channels`, reads it with `read_channel`
(scoped to the last `AGENTBOX_SLACK_LOOKBACK_DAYS`, or a `since_days` you ask
for), follows threads with `read_thread`, and searches with `search_messages`.
User IDs and `<@mentions>` are resolved to names. Posting to Slack is
intentionally not supported yet — it will come as a separate, confirmation-gated
capability.

Set `AGENTBOX_SLACK_USER` to your Slack display name/handle so briefings can
surface messages directed at *you* — the agent is told your handle and searches
for it (otherwise it can't tell which messages are mentions of you).

## 9. Todos & notes

agentbox keeps these as plain markdown in two folders under the workspace:
`todos/` (`todos.md` + a dated `done/` archive) and `notes/` (`inbox.md` for
free-form notes). Just ask:

```sh
make compose-run TASK="add a todo to call the dentist and note that I want to batch the briefings"
make compose-run TASK="show my todos, then mark the dentist one done"
```

Because they're plain files, you can also open and edit them in any editor —
flip `- [ ]` to `- [x]` in `todos.md` to mark something done. From the CLI:

```sh
docker compose exec agentbox-scheduler agentbox todo "call the dentist"   # add (no API key needed)
docker compose exec agentbox-scheduler agentbox todos                     # list open todos
docker compose exec agentbox-scheduler agentbox done "the dentist call"   # complete (model matches it)
```

`todo`/`todos` just touch the store (instant, deterministic); `done` uses the
model to match your description to the right open todo and refuses if it's
ambiguous. Completing a todo **moves** it out of `todos.md` into a dated
`done/<date>.md` file, so the active list stays short while finished work is
archived by day (`agentbox todos` shows open items; the agent can still surface
recent done via `list_todos` with `include_done`).

Or run **`./agentbox.sh`** from the folder with `docker-compose.yml` for a menu
that wraps all of this (add/show/complete todos, run a briefing, process photos,
scheduler start/stop/logs, view the journal, **ask anything**) — no commands to
memorize.

**Asking open questions.** A one-shot task is an open RAG-backed prompt: the
agent answers using your long-term memory plus its tools. Use the menu's "Ask
agentbox anything", or:

```sh
docker compose exec agentbox-scheduler agentbox "what did I plan to follow up on after Seattle?"
```

Each run is independent (memory carries context across runs, but it's not a
multi-turn conversation).

## 10. Capture from a photo

Snap a photo of a handwritten list and let Claude's vision read it and file the
items. Point the capture inbox at a synced folder so you can drop photos from
your phone:

```
AGENTBOX_CAPTURE_HOST=/Users/you/Library/Mobile Documents/com~apple~CloudDocs/agentbox-inbox
```

Drop an image there, then:

```sh
docker compose run --rm agentbox process-captures
```

Each image's todos/notes are filed, and the image is then **deleted** (captures
are transient — often photos of personal notes — so no copies are kept; only
failures are retained in a `failed/` subfolder for inspection). Tip: handwriting
recognition is usually good but depends on legibility. To do this automatically,
see the schedule below.

## 11. Run it on a schedule

This is what makes agentbox "run your day." Create a schedule:

```sh
mkdir -p config
cp schedule.example.yaml config/schedule.yaml   # then edit
```

These are **built-in tasks** — you only pick *when* each runs. Each task is just
a `name` (which selects the behavior) and a `schedule` (standard cron like
`0 8 * * *`, or a descriptor like `@daily`). You don't write any prompts:

```yaml
tasks:
  - name: process-captures
    schedule: "50 7,12,17 * * *"   # just before each briefing
  - name: daily-briefing
    schedule: "0 8,13,18 * * *"    # one task, adapts to time of day
  - name: weekly-review
    schedule: "0 17 * * 5"
```

Built-in names: `daily-briefing` (email + calendar + todos), `weekly-review`,
and `process-captures`. The prompts live in the binary, so the schedule file
stays simple. Keep **one** `daily-briefing` (not separate morning/midday/evening
tasks): it adapts to the time of day itself, and at startup every daily task runs
once — three briefing tasks would all fire back to back.

Advanced — to change what a task does, two ways:
- **Override file** (recommended): drop a markdown file named after the task in
  `config/prompts/` (e.g. `config/prompts/daily-briefing.md`). Its contents
  replace the built-in prompt, re-read each run so edits apply without a rebuild.
  A file for a new task name works too — just list the name + schedule here.
- **Inline `prompt:`** on the task — free-form instructions; takes precedence
  over the built-in and the override file.

Start the long-lived scheduler (and follow its output):

```sh
docker compose up -d        # starts Ollama + the scheduler
make compose-logs           # watch it
docker compose down         # stop
```

Test a task immediately, without waiting for its time:

```sh
make compose-run-task NAME=daily-briefing
```

Schedules fire in `AGENTBOX_TIMEZONE`. On startup the scheduler also runs every
task that fires at least once a day a single time right away, so starting (or
restarting) it delivers today's briefings and processes any waiting captures
without waiting for the next cron time. Weekly and monthly tasks (e.g. a Friday
review) are not run at startup.

**Daily output file.** Each scheduled task's result is appended to
`journal/YYYY-MM-DD.md` (one file per day) under a timestamped heading — the
assistant's "delivery" without email/push. Read today's file to catch the
morning briefing etc. Set the location with `AGENTBOX_JOURNAL_DIR` (host mount
`AGENTBOX_JOURNAL_HOST` in compose).

## 12. Personal vs work

Run agentbox separately on each machine and isolate their memory with
`AGENTBOX_NAMESPACE`:

```
AGENTBOX_NAMESPACE=personal     # on your personal machine
AGENTBOX_NAMESPACE=work         # on your work machine
```

Namespaces never read each other's memory, and you mount each context's own
folders/accounts. This keeps work and personal cleanly separated.

## Privacy

- **Local:** your long-term memory and the embeddings that index it stay in a
  volume on your machine. The vector store runs in-process; nothing about your
  memory is sent anywhere.
- **Not local:** the language model runs in the cloud — Claude (Anthropic) or
  Gemini (Google), depending on `AGENTBOX_MODEL`. Anything the agent reasons over
  in a given step — an email it reads, a file's contents, a photo you capture —
  is sent to that provider at that moment. If that matters for some data, don't
  mount/connect it, or review the provider's data-handling options.
- **Trust boundary:** the agent runs model-directed shell commands inside the
  container as a non-root user, and only sees what you mount. Mount only what
  you're comfortable letting it read and change.

## 12b. Publishing the image (optional)

To run the same build on multiple machines, publish it to Docker Hub. No secrets
are baked into the image (config comes from `.env` at runtime), so the repo can
be public.

```sh
docker login -u machunter          # use a Docker Hub access token as the password
make publish VERSION=0.1.0          # multi-arch build + push as machunter/agentbox
```

`make publish` builds for `linux/amd64` and `linux/arm64` (so it runs on Intel
and Apple Silicon) and tags both `:0.1.0` and `:latest`. Override the repo with
`HUB_IMAGE=youruser/agentbox` if needed.

To make the stack *pull* the published image instead of building locally, set
`image: machunter/agentbox:latest` on the `agentbox` and `agentbox-scheduler`
services in `docker-compose.yml`.

## 14. Troubleshooting

**"credit balance is too low" (HTTP 400)** — your Anthropic key has no credits.
Add credits under Plans & Billing.

**Gemini "quota exceeded" / HTTP 429 (RESOURCE_EXHAUSTED)** — the model isn't
available on your tier. `gemini-2.5-pro` typically needs billing enabled on your
Google project; `gemini-2.5-flash` works on the free tier. Switch with
`AGENTBOX_MODEL=gemini-2.5-flash` or enable billing.

**Email won't connect / "unexpected EOF" / login fails** — the app password
likely has spaces; remove them. Confirm you used an *app* password (not your
account password) and that IMAP is enabled. Diagnose with `mail-check`; for the
raw protocol, set `AGENTBOX_IMAP_DEBUG=1`.

**`[memory: disabled …]`** — the embedder (Ollama) wasn't reachable. With Docker
it should start automatically; natively, run `ollama pull nomic-embed-text`. The
agent still runs, just without long-term memory.

**Calendar shows nothing / wrong day** — check the ICS URL with `cal-check`, and
set `AGENTBOX_TIMEZONE` to your zone (e.g. `America/Los_Angeles`).

**`cal-check: … fetch failed: … (Client.Timeout exceeded…)`** — the ICS feed is
too big to download in time (busy work calendars can be tens of MB). Raise the
timeout, e.g. `AGENTBOX_CAL_TIMEOUT=120`, and recreate the container. `cal-check`
now reports the real cause (TLS, DNS, timeout) instead of a bare "fetch failed".

**Docker errors like "read-only file system" or layer-register failures** — the
Docker Desktop disk is full or wedged. Restart Docker Desktop and/or run
`docker system prune` to reclaim space, then retry.

**`resource deadlock avoided` reading a mounted file (macOS)** — a **single-file**
bind mount trips a Docker VirtioFS bug when the file carries macOS extended
attributes (`com.apple.provenance`, `com.apple.macl`), which `xattr -c` can't
fully strip. The compose files avoid this by mounting the **`config/` directory**
(holding `schedule.yaml`) rather than the file itself — directory mounts aren't
affected. If you still hit it on an older config that mounts the file directly,
either switch to the directory mount or, as a stopgap, switch Docker Desktop's
file-sharing implementation (Settings → General) from **VirtioFS** to **gRPC
FUSE**. (`xattr -l <file>` shows what's attached.)

**Captured photos aren't becoming todos** — run with `AGENTBOX_DEBUG=1` and look
for `skip entry … reason="unsupported file type"`. The usual cause is **iPhone
HEIC photos** (only `jpeg/png/gif/webp/pdf` are supported). Set iPhone → Settings
→ Camera → Formats → **"Most Compatible"** to capture JPEG.

**Turn on verbose logging** — `AGENTBOX_DEBUG=1` traces the model/provider, every
tool call *and result*, memory searches, and capture decisions to stderr.

**The agent can't see my file** — it's only mounted if it's under `/workspace`
(or a folder you added via `MOUNTS` / the override file). Refer to it by its
container path.

## 15. Reference

### Make targets

| Command | What it does |
|---|---|
| `make compose-run TASK="…"` | Run a one-shot task (Docker). `MOUNTS="h:c …"` adds folders. |
| `make compose-serve` | Start the scheduler daemon (+ Ollama). |
| `make compose-logs` | Follow the scheduler's logs. |
| `make compose-run-task NAME=…` | Run one scheduled task now. |
| `make compose-down` | Stop the stack. |
| `make build` / `make run TASK="…"` / `make test` | Native build / run / tests. |

### Subcommands (native binary or `--entrypoint agentbox`)

| Command | What it does |
|---|---|
| `agentbox "<task>"` | One-shot task. |
| `agentbox serve` | Run the scheduler daemon. |
| `agentbox run-task <name>` | Run one scheduled task now. |
| `agentbox process-captures` | Process the capture inbox. |
| `agentbox todo "<text>"` | Add a todo (no model/API key needed — appends to the store). |
| `agentbox todos` | List open todos (no model needed). |
| `agentbox done "<which todo>"` | Mark a todo done; the model matches your description to the right open todo. |
| `agentbox mail-check` / `cal-check` | Test email / calendar setup (no API call). |
| `agentbox version` | Print the build version, git commit, and build date. |

### Environment variables

| Variable | Purpose |
|---|---|
| `AGENTBOX_MODEL` | Model to use (`claude-*` or `gemini-*`). Default `claude-opus-4-8`. |
| `ANTHROPIC_API_KEY` | Claude API key (needs credits) — for `claude-*` models. |
| `GEMINI_API_KEY` / `GOOGLE_API_KEY` | Google API key — for `gemini-*` models. |
| `AGENTBOX_NAMESPACE` | Memory isolation (`personal` / `work`). Default `default`. |
| `AGENTBOX_WORKSPACE` | Host dir mounted at `/workspace`. Default current dir. |
| `AGENTBOX_TOOLS_HOST` | Host dir for the agent's persistent, self-built tool library (default `./tools`). |
| `MOUNTS` (make var) | Extra `host:container` mounts for a run. |
| `AGENTBOX_MEMORY_DIR` | Where the vector store persists. |
| `AGENTBOX_EMBED_MODEL` / `AGENTBOX_OLLAMA_URL` | Embedding model / Ollama URL. |
| `AGENTBOX_IMAP_HOST` / `_PORT` / `_USER` / `_PASS` | Email (read-only); app password. |
| `AGENTBOX_EMAIL_SINCE_DAYS` | Minimum lookback window (days) for email tools; the agent can widen but not narrow it; 0/unset = no date limit. |
| `AGENTBOX_MAIL_STATE_DIR` | Where `list_new_emails` stores per-mailbox UID watermarks (default: the memory dir). |
| `AGENTBOX_ICS_URLS` | Calendar ICS feed URLs (comma-separated). |
| `AGENTBOX_CAL_EMAIL` | Your own calendar address(es) (comma-separated); enables per-event RSVP status. Defaults to `AGENTBOX_IMAP_USER`. |
| `AGENTBOX_CAL_TIMEOUT` | Seconds to fetch an ICS feed (default 60); raise for large feeds. |
| `AGENTBOX_CAL_CACHE_TTL` | Seconds to reuse a cached ICS feed before revalidating (default 900); `0` = always revalidate. |
| `AGENTBOX_SLACK_TOKEN` | Slack token; enables the Slack tools. User token (`xoxp-`) for search; bot token (`xoxb-`) for the rest. |
| `AGENTBOX_SLACK_USER` | Your Slack display name/handle, so briefings can find messages directed at you. |
| `AGENTBOX_SLACK_LOOKBACK_DAYS` | Default `read_channel` history lookback (days); `0`/unset = count-based only. |
| `AGENTBOX_SLACK_TIMEOUT` | Seconds for a single Slack API request (default 30). |
| `AGENTBOX_TIMEZONE` | Timezone for day boundaries and cron (e.g. `America/Los_Angeles`). |
| `AGENTBOX_TODOS_DIR` | Where `todos.md` + `done/` live (default `todos/`). |
| `AGENTBOX_NOTES_DIR` | Where `inbox.md` (free-form notes) lives (default `notes/`). |
| `AGENTBOX_CAPTURE_DIR` / `AGENTBOX_CAPTURE_HOST` | Capture inbox (container path / host folder). |
| `AGENTBOX_SCHEDULE` / `AGENTBOX_SCHEDULE_FILE` | Schedule YAML path (in-container / host). |
| `AGENTBOX_MAX_TOOL_CALLS` | Max tool-call rounds per run before it stops and summarizes (default 50). Raise for busy work mailboxes. |
| `AGENTBOX_DEBUG` | `1` = verbose debug logging to stderr (off by default). |
| `AGENTBOX_JOURNAL_DIR` / `AGENTBOX_JOURNAL_HOST` | Daily-output markdown files (one per day) / host mount. |

For architecture and design rationale, see [PRD.md](PRD.md); for a shorter
overview, see [README.md](README.md).
