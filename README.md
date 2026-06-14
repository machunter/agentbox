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
  ADK's `mcptoolset` — the pattern for adding external connectors (email,
  calendar) later. It gives the model cleaner, read-only file primitives
  alongside `run_bash`.
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

## Configuration

| Variable | Default | Purpose |
|---|---|---|
| `ANTHROPIC_API_KEY` | — | Required. Claude API key. |
| `AGENTBOX_NAMESPACE` | `default` | Isolates memory per deployment (e.g. `personal`, `work`). |
| `AGENTBOX_MEMORY_DIR` | `~/.agentbox/memory` | Where the vector store persists. |
| `AGENTBOX_OLLAMA_URL` | Ollama's default | Embedder base URL (compose sets the service URL). |
| `AGENTBOX_EMBED_MODEL` | `nomic-embed-text` | Local embedding model. |
| `AGENTBOX_WORKSPACE` | `.` | Host dir mounted at `/workspace` (compose only). |

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
