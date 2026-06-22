#!/usr/bin/env bash
# agentbox — a small menu UX over the agentbox container.
#
# Run it from the folder that holds docker-compose.yml:
#   ./agentbox.sh          (or: bash agentbox.sh)
#
# It wraps `docker compose`: when the scheduler is running it execs into it,
# otherwise it spins up a one-off container. No Makefile or source checkout
# needed — just Docker + bash.

set -uo pipefail
cd "$(dirname "$0")"

# Docker Compose v2 (preferred) or v1.
if docker compose version >/dev/null 2>&1; then
  DC=(docker compose)
elif command -v docker-compose >/dev/null 2>&1; then
  DC=(docker-compose)
else
  echo "Docker Compose not found. Install Docker Desktop, then run this from the folder with docker-compose.yml." >&2
  exit 1
fi

scheduler_running() {
  [ -n "$("${DC[@]}" ps --status running -q agentbox-scheduler 2>/dev/null)" ]
}

# Run an agentbox subcommand: exec into the running scheduler if it's up,
# else a one-off container.
abx() {
  if scheduler_running; then
    "${DC[@]}" exec agentbox-scheduler agentbox "$@"
  else
    "${DC[@]}" run --rm agentbox "$@"
  fi
}

# For commands that need the scheduler container specifically (its schedule +
# config mounts), e.g. run-task.
abx_scheduler() {
  if scheduler_running; then
    "${DC[@]}" exec agentbox-scheduler agentbox "$@"
  else
    echo "The scheduler isn't running — start it first (option 6)." >&2
  fi
}

show_journal() {
  if scheduler_running; then
    "${DC[@]}" exec agentbox-scheduler sh -c \
      'ls -t /data/journal/*.md 2>/dev/null | head -1 | xargs cat 2>/dev/null || echo "(no journal yet)"'
  else
    local f
    f=$(ls -t ./journal/*.md 2>/dev/null | head -1)
    if [ -n "$f" ]; then cat "$f"; else echo "(no journal yet)"; fi
  fi
}

pause() { read -rp $'\nPress Enter to continue… ' _; }

while true; do
  clear 2>/dev/null || true
  scheduler_running && st="running" || st="stopped"
  cat <<MENU
agentbox ▸  (scheduler: $st)

  1) Add a todo
  2) Mark a todo done
  3) Show todos
  4) Run a briefing now
  5) Process captured photos
  6) Scheduler: start
  7) Scheduler: stop
  8) Scheduler: recent logs
  9) View latest journal
  q) Quit
MENU
  read -rp "> " choice
  case "${choice:-}" in
    1) read -rp "Todo text: " t; [ -n "${t:-}" ] && abx todo "$t"; pause ;;
    2) read -rp "Which todo (a few words): " d; [ -n "${d:-}" ] && abx done "$d"; pause ;;
    3) abx todos; pause ;;
    4) abx_scheduler run-task daily-briefing; pause ;;
    5) abx process-captures; pause ;;
    6) "${DC[@]}" up -d; pause ;;
    7) "${DC[@]}" stop agentbox-scheduler; pause ;;
    8) "${DC[@]}" logs --tail=80 agentbox-scheduler; pause ;;
    9) show_journal; pause ;;
    q | Q) exit 0 ;;
    *) echo "Unknown option: ${choice:-}"; pause ;;
  esac
done
