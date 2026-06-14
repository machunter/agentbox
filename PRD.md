# agentbox — Product Requirements (living document)

> **Reverse PRD.** Written after the fact to capture what agentbox is, why each
> choice was made, and where it's going. It is updated as the product evolves —
> treat it as the source of intent, not a frozen spec.

**Status key:** ✅ Shipped (validated live) · 🟡 Shipped (unit-tested, not exercised live) · 🔵 Planned · 🤔 Considering
**Last updated:** 2026-06-14 (scheduler) · **Branch of record:** `main`

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
   deliberate, disclosed exception is **LLM inference** (Claude via the Anthropic
   API): content the agent reasons over is sent to Anthropic at inference time.
   "Local" applies to memory, not inference.
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
| Agentic loop (perceive→think→act) on Google ADK Go | ✅ | Claude Opus 4.8 via `adk-anthropic-go`; `maxTurns` safety cap |
| `run_bash` tool | ✅ | Shell in the container; 60s per-command timeout |
| Long-term memory (recall across runs) | ✅ | Auto-recall (`preloadmemorytool`) + on-demand (`loadmemorytool`); persisted after each run |
| Filesystem tools (`list_directory`, `read_file`, `search_files`) | ✅ | MCP server jailed to `/workspace` |
| Multi-directory access | ✅ | `MOUNTS` / compose override; mount under `/workspace` |
| Email — read (`list_recent_emails`, `search_emails`, `read_email`) | ✅ | IMAP, read-only; enabled only when configured |
| Calendar — read (`list_upcoming_events`, `events_on_day`, `search_events`) | ✅ | ICS feeds, read-only, recurrence-expanded, all-day aware; validated live |
| Long-lived / scheduled operation (`serve`, `run-task`) | ✅ | Cron scheduler runs YAML-configured tasks; run path validated live via `run-task`, timed firing via robfig/cron |
| Email — send | 🔵 | Gated by human confirmation; design pending (§8) |
| Local LLM inference | 🤔 | Would remove the inference caveat; large effort, out of scope for now |

## 6. Architecture (as built)

- **Language/runtime:** Go (1.25+), single binary, Docker.
- **Agent framework:** Google **ADK Go** (`google.golang.org/adk`) — runner +
  `llmagent`; tools and toolsets registered on the agent.
- **Model:** Claude **Opus 4.8** (`claude-opus-4-8`) with adaptive thinking, via
  the community **`adk-anthropic-go`** adapter (fallback: a thin `model.LLM`
  wrapper over `anthropic-sdk-go`).
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
| `AGENTBOX_MEMORY_DIR` | Vector-store location. |
| `AGENTBOX_OLLAMA_URL`, `AGENTBOX_EMBED_MODEL` | Local embedder. |
| `AGENTBOX_WORKSPACE`, `MOUNTS` | Host directories the agent can access. |
| `AGENTBOX_IMAP_HOST/PORT/USER/PASS` | Read-only email (use an app password). |
| `AGENTBOX_ICS_URLS`, `AGENTBOX_TIMEZONE` | Read-only calendar (ICS feeds) + day-boundary / cron timezone. |
| `AGENTBOX_SCHEDULE` | Path to the schedule YAML (for `serve` / `run-task`). |

## 8. Open questions / decisions pending

- **Send-confirmation channel.** Human-in-the-loop confirmation (`mcptoolset`'s
  `RequireConfirmation`) needs an approval path, but the agent is a one-shot,
  non-interactive container today. Resolving this is a prerequisite for SMTP send
  and any other write action.
- **Scheduler result delivery.** Scheduled runs currently log their output. A
  real assistant probably wants results delivered (email/push/file) rather than
  only sitting in logs — pairs naturally with the email-send capability.
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
