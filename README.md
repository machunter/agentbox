# agentbox

An autonomous AI agent, written in Go, that runs inside a Docker container.

Give it a task. It loops with Claude — reasoning and running `bash` commands in
its sandboxed container — until the task is done, then prints a short summary.
It remembers past sessions in a local vector store, so context carries across
runs without anything leaving the machine.

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
  (`list_recent_emails`, `search_emails`, `read_email`), launched the same way.
  Enabled only when IMAP credentials are configured; otherwise silently skipped.
- **`internal/mcpcal`** — a read-only calendar MCP server over iCal (ICS) feeds
  (`list_upcoming_events`, `events_on_day`, `search_events`), expanding recurring
  events within the query window. Enabled only when feed URLs are configured.
- **`internal/memory`** — a local implementation of ADK's `memory.Service`: it
  embeds session content and stores it in an embedded [chromem-go](https://github.com/philippgille/chromem-go)
  vector database, then retrieves it by semantic similarity. The agent gets
  relevant memories injected automatically and can also search them on demand.

The model is `claude-opus-4-8` with adaptive thinking. A turn cap (`maxTurns`)
and a per-command timeout keep a run bounded.

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
> but the **LLM is Claude via the Anthropic API** — so the content the agent
> reasons over is still sent to Anthropic at inference time. "Local" applies to
> memory, not inference.

## Prerequisites

- An Anthropic API key
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

## Scheduler (long-lived mode)

Instead of one-shot tasks, agentbox can run as a long-lived process that executes
tasks on cron schedules — e.g. a daily morning briefing that reads your email and
calendar. Define tasks in a YAML file:

```sh
cp schedule.example.yaml schedule.yaml   # then edit names, cron specs, prompts
docker compose up -d                      # starts Ollama + the scheduler
make compose-logs                         # follow the scheduler's output
```

Each task is `{name, schedule, prompt}`; `schedule` is standard 5-field cron
(`0 8 * * *`) or a descriptor (`@daily`). Each run is an independent agent run
that shares the persistent memory store. Set `AGENTBOX_TIMEZONE` so schedules
fire in your local time.

Test a task without waiting for its time:

```sh
make compose-run-task NAME=morning-briefing
```

Locally (no Docker): `AGENTBOX_SCHEDULE=schedule.yaml agentbox serve` (or
`agentbox run-task <name>`).

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `ANTHROPIC_API_KEY` | — | Required. Claude API key. |
| `AGENTBOX_NAMESPACE` | `default` | Isolates memory per deployment (e.g. `personal`, `work`). |
| `AGENTBOX_MEMORY_DIR` | `~/.agentbox/memory` | Where the vector store persists. |
| `AGENTBOX_OLLAMA_URL` | Ollama's default | Embedder base URL (compose sets the service URL). |
| `AGENTBOX_EMBED_MODEL` | `nomic-embed-text` | Local embedding model. |
| `AGENTBOX_WORKSPACE` | `.` | Host dir mounted at `/workspace` (compose only). |
| `AGENTBOX_IMAP_HOST` | — | IMAP server; set (with user/pass) to enable email tools. |
| `AGENTBOX_IMAP_PORT` | `993` | IMAP port (implicit TLS). |
| `AGENTBOX_IMAP_USER` | — | IMAP username. |
| `AGENTBOX_IMAP_PASS` | — | IMAP password (use an app password). |
| `AGENTBOX_ICS_URLS` | — | Calendar ICS feed URLs (comma-separated); enables calendar tools. |
| `AGENTBOX_TIMEZONE` | `UTC` | Timezone for day boundaries, event times, and cron schedules. |
| `AGENTBOX_SCHEDULE` | — | Path to the schedule YAML (required for `serve` / `run-task`). |

### Email (read-only)

Set `AGENTBOX_IMAP_HOST`, `AGENTBOX_IMAP_USER`, and `AGENTBOX_IMAP_PASS` (e.g. in
`.env`) to give the agent read-only email tools: `list_recent_emails`,
`search_emails`, and `read_email`. Use a provider **app password**, not your main
password. Sending is intentionally not supported yet — it will come as a
separate, confirmation-gated capability. Email stays subject to the same privacy
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
