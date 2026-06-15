IMAGE := agentbox

# Docker Hub image for publishing. Override IMAGE/VERSION as needed:
#   make publish VERSION=0.1.0
HUB_IMAGE ?= machunter/agentbox
VERSION ?= latest

.PHONY: build run test docker-build docker-run compose-run compose-serve compose-logs compose-run-task compose-down publish tidy clean

# Scheduler config file (host path), used by compose-run-task.
SCHEDULE_FILE ?= schedule.yaml

# Build the local binary.
build:
	go build -o agentbox .

# Run locally (reads ANTHROPIC_API_KEY from the environment).
# Usage: make run TASK="list files and summarize this project"
run: build
	./agentbox "$(TASK)"

test:
	go test ./...

tidy:
	go mod tidy

# Build the container image.
docker-build:
	docker build -t $(IMAGE) .

# Run the agent in a container. Mounts the current dir as /workspace so the
# agent can act on real files; pass the task via TASK.
# Usage: make docker-run TASK="create hello.txt with the current date"
docker-run:
	docker run --rm -it \
		-e ANTHROPIC_API_KEY \
		-v "$(PWD):/workspace" \
		$(IMAGE) "$(TASK)"

# Extra host directories to grant the agent, as space-separated host:container
# pairs. Mount them UNDER /workspace so the jailed file tools can see them too.
# Usage: make compose-run MOUNTS="$$HOME/Notes:/workspace/notes" TASK="..."
MOUNTS ?=

# Run the full stack (agent + local Ollama for memory) via docker compose.
# Brings up Ollama, pulls the embedding model, then runs the agent. Memory
# persists in a named volume across runs.
# Usage: make compose-run TASK="summarize the files in /workspace"
compose-run:
	docker compose run --rm $(addprefix -v ,$(MOUNTS)) agentbox "$(TASK)"

# Start the long-lived scheduler (+ Ollama). Reads ./schedule.yaml.
compose-serve:
	docker compose up -d --build

# Follow the scheduler's logs.
compose-logs:
	docker compose logs -f agentbox-scheduler

# Run one configured task immediately (handy for testing schedule.yaml).
# Usage: make compose-run-task NAME=morning-briefing
compose-run-task:
	docker compose run --rm \
		-v "$(CURDIR)/$(SCHEDULE_FILE):/etc/agentbox/schedule.yaml:ro" \
		-e AGENTBOX_SCHEDULE=/etc/agentbox/schedule.yaml \
		agentbox run-task "$(NAME)"

# Stop the stack. Add `-v` manually to also drop the memory/model volumes.
compose-down:
	docker compose down

# Build a multi-arch image and push it to Docker Hub. Run `docker login` first.
# Usage: make publish VERSION=0.1.0   (also tags :latest)
publish:
	docker buildx build --platform linux/amd64,linux/arm64 \
		-t $(HUB_IMAGE):$(VERSION) -t $(HUB_IMAGE):latest --push .

clean:
	rm -f agentbox
