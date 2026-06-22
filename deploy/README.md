# Deploy agentbox from the published image

Run agentbox on any machine with **only Docker** — no source checkout, no Go, no
build. Everything here pulls the published `machunter/agentbox` image.

## Setup

Copy these three files into a folder on the target machine (or copy the whole
`deploy/` folder):

- `docker-compose.yml`
- `.env.example`  → copy to `.env`
- `schedule.example.yaml`  → copy to `config/schedule.yaml` (only if you want the scheduler)
- `config/prompts/`  → editable prompt files for the built-in tasks (optional; copy if you want to tweak them)

```sh
cp .env.example .env                              # then edit: set AGENTBOX_MODEL + your API key
mkdir -p config                                   # only if running the scheduler:
cp schedule.example.yaml config/schedule.yaml     #   the schedule is mounted as the config/ dir
docker compose pull                               # grab the agentbox + ollama images
```

> **macOS:** the schedule is mounted as the **`config/` directory**, not a single
> file, which sidesteps a Docker VirtioFS bug (`resource deadlock avoided`) caused
> by macOS extended attributes. If you still hit a file-sharing error, switch
> Docker Desktop → Settings → General → file sharing implementation to **gRPC FUSE**.

## Run

```sh
# One-shot task:
docker compose run --rm agentbox "summarize the files in /workspace"

# Long-lived scheduler (needs config/schedule.yaml — see Setup):
docker compose up -d                        # starts ollama + the scheduler
docker compose logs -f agentbox-scheduler   # watch it
docker compose down                         # stop
```

## Quick commands (todos, etc.)

No Makefile needed — run agentbox subcommands with plain `docker compose`. If the
scheduler is up, exec into it; otherwise use a one-off container:

```sh
# scheduler running (docker compose up -d):
docker compose exec agentbox-scheduler agentbox todo "call the dentist"   # add a todo (no API key needed)
docker compose exec agentbox-scheduler agentbox done "the dentist call"   # mark one done (model matches it)

# not running a scheduler:
docker compose run --rm agentbox todo "call the dentist"
```

These write to the same notes store the briefings use. Frequent user? Alias it:
`alias abx='docker compose exec agentbox-scheduler agentbox'`, then `abx todo "…"`.

## Tasks & prompts

The schedule lists **built-in tasks** — you just choose when each runs (a name +
a cron schedule, no prompts). Built-ins: `daily-briefing`, `weekly-review`,
`process-captures`.

To customize what a task says, edit its prompt file in **`config/prompts/`** —
this bundle ships the current ones (`daily-briefing.md`, `weekly-review.md`, and
`process-captures.md`, which is the image-extraction instruction) ready to edit.
Changes apply on the next run (no rebuild); these files override the prompts
built into the image. You can also add `config/prompts/<name>.md` for a
brand-new task name.

> Use **`docker compose`** directly here — the `make compose-*` shortcuts from
> the [full repo](https://github.com/machunter/agentbox) are not part of this
> bundle (there's no Makefile). The mapping: `make compose-run` →
> `docker compose run --rm agentbox "…"`, `make compose-serve` →
> `docker compose up -d`, `make compose-down` → `docker compose down`.

## Notes

- **`.env` lives in this folder** and holds your keys — keep it private; don't
  commit it.
- **What the agent can see:** by default it acts on this folder (mounted at
  `/workspace`). Set `AGENTBOX_WORKSPACE=/path/to/your/files` in `.env` to point
  it elsewhere.
- **Memory** (Ollama + the vector store) runs automatically and persists in
  Docker named volumes across runs.
- **Pin a version:** set `AGENTBOX_IMAGE=machunter/agentbox:0.1.0` in `.env`.
- Full feature docs: see the project [MANUAL](https://github.com/machunter/agentbox/blob/main/MANUAL.md).
