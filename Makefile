IMAGE := agentbox

.PHONY: build run docker-build docker-run tidy clean

# Build the local binary.
build:
	go build -o agentbox .

# Run locally (reads ANTHROPIC_API_KEY from the environment).
# Usage: make run TASK="list files and summarize this project"
run: build
	./agentbox "$(TASK)"

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

clean:
	rm -f agentbox
