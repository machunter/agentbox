# Deploy agentbox from the published image

Run agentbox on any machine with **only Docker** — no source checkout, no Go, no
build. Everything here pulls the published `machunter/agentbox` image.

## Setup

Copy these three files into a folder on the target machine (or copy the whole
`deploy/` folder):

- `docker-compose.yml`
- `.env.example`  → copy to `.env`
- `schedule.example.yaml`  → copy to `schedule.yaml` (only if you want the scheduler)

```sh
cp .env.example .env        # then edit: set AGENTBOX_MODEL + your API key
xattr -cr .                 # macOS only: strip extended attrs from downloaded files
docker compose pull         # grab the agentbox + ollama images
```

> **macOS:** downloaded/copied files often carry extended attributes that break
> Docker's file sharing (you'd see `resource deadlock avoided` when the scheduler
> reads `schedule.yaml`). `xattr -cr .` clears them for the whole folder; for a
> single file, `xattr -c schedule.yaml`.

## Run

```sh
# One-shot task:
docker compose run --rm agentbox "summarize the files in /workspace"

# Long-lived scheduler (needs schedule.yaml):
cp schedule.example.yaml schedule.yaml      # then edit
docker compose up -d                        # starts ollama + the scheduler
docker compose logs -f agentbox-scheduler   # watch it
docker compose down                         # stop
```

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
