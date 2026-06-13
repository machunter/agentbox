# ---- build stage ----
FROM golang:1.24-bookworm AS build
WORKDIR /src

# Cache dependencies first.
COPY go.mod go.sum ./
RUN go mod download

# Build a static binary.
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /out/agentbox .

# ---- runtime stage ----
# Debian slim (not scratch/distroless) because the agent's run_bash tool needs
# a real shell and core utilities to be useful.
FROM debian:bookworm-slim
RUN apt-get update \
    && apt-get install -y --no-install-recommends bash coreutils ca-certificates \
    && rm -rf /var/lib/apt/lists/*

# Run as an unprivileged user — the agent executes model-directed commands, so
# keep its blast radius small.
RUN useradd --create-home --uid 10001 agent
USER agent
WORKDIR /workspace

COPY --from=build /out/agentbox /usr/local/bin/agentbox

ENTRYPOINT ["agentbox"]
