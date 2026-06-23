# ---- build stage ----
FROM golang:1.25-bookworm AS build
WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

# Build a static binary, stamping version/commit/date (passed as build args by
# the Makefile; default to a dev build otherwise).
ARG AGENTBOX_VERSION=dev
ARG AGENTBOX_COMMIT=none
ARG AGENTBOX_DATE=unknown
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build \
    -ldflags="-s -w -X main.version=${AGENTBOX_VERSION} -X main.commit=${AGENTBOX_COMMIT} -X main.buildDate=${AGENTBOX_DATE}" \
    -o /out/agentbox .

# ---- runtime stage ----
# Debian slim (not scratch/distroless) because the agent's run_bash tool needs
# a real shell and core utilities to be useful.
FROM debian:bookworm-slim
# tzdata so AGENTBOX_TIMEZONE (e.g. America/Los_Angeles) resolves — without it
# time.LoadLocation fails and the scheduler/journal silently fall back to UTC.
# python3 is the agent's one sanctioned scripting language: its standard library
# (urllib, json, subprocess, …) covers most scripting, so the agent uses it
# deliberately instead of probing perl/python/openssl every run.
RUN apt-get update \
    && apt-get install -y --no-install-recommends bash coreutils ca-certificates tzdata python3 \
    && rm -rf /var/lib/apt/lists/*

# Run as an unprivileged user — the agent executes model-directed commands, so
# keep its blast radius small. Create the long-term-memory dir owned by that
# user so a mounted named volume inherits writable ownership on first init.
RUN useradd --create-home --uid 10001 agent \
    && mkdir -p /data/memory \
    && chown -R agent:agent /data
USER agent
WORKDIR /workspace

# Default the memory store into the volume mount point (overridable at runtime).
ENV AGENTBOX_MEMORY_DIR=/data/memory

COPY --from=build /out/agentbox /usr/local/bin/agentbox

ENTRYPOINT ["agentbox"]
