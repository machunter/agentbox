IMAGE := agentbox

.PHONY: build run test docker-build docker-run compose-run compose-down tidy clean

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

# Run the full stack (agent + local Ollama for memory) via docker compose.
# Brings up Ollama, pulls the embedding model, then runs the agent. Memory
# persists in a named volume across runs.
# Usage: make compose-run TASK="summarize the files in /workspace"
compose-run:
	docker compose run --rm agentbox "$(TASK)"

# Stop the stack. Add `-v` manually to also drop the memory/model volumes.
compose-down:
	docker compose down

clean:
	rm -f agentbox
