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
8. [Todos & notes](#8-todos--notes)
9. [Capture from a photo](#9-capture-from-a-photo)
10. [Run it on a schedule](#10-run-it-on-a-schedule)
11. [Personal vs work](#11-personal-vs-work)
12. [Privacy](#privacy)
13. [Troubleshooting](#13-troubleshooting)
14. [Reference](#14-reference)

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

## 8. Todos & notes

agentbox keeps your todos and notes as plain markdown (`todos.md`, `inbox.md`)
in a notes folder (default `notes/` under the workspace). Just ask:

```sh
make compose-run TASK="add a todo to call the dentist and note that I want to batch the briefings"
make compose-run TASK="show my todos, then mark the dentist one done"
```

Because they're plain files, you can also open and edit them in any editor.

## 9. Capture from a photo

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

Each image's todos/notes are filed, and the image is moved to a `processed/`
subfolder (failures go to `failed/`). Tip: handwriting recognition is usually
good but depends on legibility. To do this automatically, see the schedule
below.

## 10. Run it on a schedule

This is what makes agentbox "run your day." Create a schedule:

```sh
cp schedule.example.yaml schedule.yaml   # then edit
```

Each task has a `name`, a `schedule` (standard cron like `0 8 * * *`, or a
descriptor like `@daily`), and either a `prompt` (an agent task) or a `command`
(a built-in such as `process-captures`):

```yaml
tasks:
  - name: morning-briefing
    schedule: "0 8 * * *"
    prompt: >
      Summarize my unread emails and today's calendar, and list my open todos.
  - name: process-captures
    schedule: "*/30 * * * *"
    command: process-captures
```

Start the long-lived scheduler (and follow its output):

```sh
docker compose up -d        # starts Ollama + the scheduler
make compose-logs           # watch it
docker compose down         # stop
```

Test a task immediately, without waiting for its time:

```sh
make compose-run-task NAME=morning-briefing
```

Schedules fire in `AGENTBOX_TIMEZONE`.

## 11. Personal vs work

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

## 13. Troubleshooting

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

**Docker errors like "read-only file system" or layer-register failures** — the
Docker Desktop disk is full or wedged. Restart Docker Desktop and/or run
`docker system prune` to reclaim space, then retry.

**The agent can't see my file** — it's only mounted if it's under `/workspace`
(or a folder you added via `MOUNTS` / the override file). Refer to it by its
container path.

## 14. Reference

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
| `agentbox mail-check` / `cal-check` | Test email / calendar setup (no API call). |

### Environment variables

| Variable | Purpose |
|---|---|
| `AGENTBOX_MODEL` | Model to use (`claude-*` or `gemini-*`). Default `claude-opus-4-8`. |
| `ANTHROPIC_API_KEY` | Claude API key (needs credits) — for `claude-*` models. |
| `GEMINI_API_KEY` / `GOOGLE_API_KEY` | Google API key — for `gemini-*` models. |
| `AGENTBOX_NAMESPACE` | Memory isolation (`personal` / `work`). Default `default`. |
| `AGENTBOX_WORKSPACE` | Host dir mounted at `/workspace`. Default current dir. |
| `MOUNTS` (make var) | Extra `host:container` mounts for a run. |
| `AGENTBOX_MEMORY_DIR` | Where the vector store persists. |
| `AGENTBOX_EMBED_MODEL` / `AGENTBOX_OLLAMA_URL` | Embedding model / Ollama URL. |
| `AGENTBOX_IMAP_HOST` / `_PORT` / `_USER` / `_PASS` | Email (read-only); app password. |
| `AGENTBOX_ICS_URLS` | Calendar ICS feed URLs (comma-separated). |
| `AGENTBOX_TIMEZONE` | Timezone for day boundaries and cron (e.g. `America/Los_Angeles`). |
| `AGENTBOX_NOTES_DIR` | Where `todos.md` / `inbox.md` live. |
| `AGENTBOX_CAPTURE_DIR` / `AGENTBOX_CAPTURE_HOST` | Capture inbox (container path / host folder). |
| `AGENTBOX_SCHEDULE` / `AGENTBOX_SCHEDULE_FILE` | Schedule YAML path (in-container / host). |

For architecture and design rationale, see [PRD.md](PRD.md); for a shorter
overview, see [README.md](README.md).
