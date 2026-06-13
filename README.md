# agentbox

An autonomous AI agent, written in Go, that runs inside a Docker container.

Give it a task. It loops with Claude — reasoning and running `bash` commands in
its sandboxed container — until the task is done, then prints a short summary.

```
┌──────────┐   task    ┌─────────────────────────────┐
│  you     │ ────────► │  agentbox (in container)    │
└──────────┘           │                             │
                       │   ┌─────► Claude (Opus 4.8)  │  think
                       │   │          │               │
                       │   │          ▼               │
                       │   └──── run_bash tool ◄────── │  act
                       └─────────────────────────────┘
```

## How it works

- **`main.go`** — reads the task (CLI arg or stdin), checks config, runs the agent.
- **`internal/agent`** — the perceive → think → act loop. Sends the conversation
  to Claude, executes the tools Claude requests, feeds results back, repeats
  until Claude stops asking for tools (or hits the turn cap).
- **`internal/tools`** — the agent's one capability today: `run_bash`, which runs
  a shell command in the container and returns its output.

The model is `claude-opus-4-8` with adaptive thinking. A turn cap (`maxTurns`)
and a per-command timeout keep a run bounded.

## Prerequisites

- An Anthropic API key
- Go 1.24+ (to run locally) and/or Docker (to run containerized)

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

## Run in Docker

```sh
make docker-build
make docker-run TASK="summarize the files in /workspace"
```

`docker-run` mounts the current directory as `/workspace` so the agent can act
on real files, passes your `ANTHROPIC_API_KEY` through, and runs as an
unprivileged user. The container ships `bash` + coreutils so `run_bash` is useful.

## Safety notes

The agent executes model-directed shell commands. That is the point — and the
reason it runs in a container as a non-root user. Treat the container as the
trust boundary: only mount directories you're comfortable letting the agent
read and modify, and review the task you give it.

## Extending it

Add a capability by defining a new tool in `internal/tools` (a `ToolParam` plus
an executor function) and wiring it into the `toolset` and the dispatch switch
in `internal/agent/agent.go`. The loop machinery doesn't change.
