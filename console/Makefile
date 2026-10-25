PORT ?= 8083
IMAGE ?= open-ads-console:local

.PHONY: help run build test docker-build docker-run docker

help:
	@printf '%s\n' \
		'Open Ads console targets:' \
		'  make run            Serve locally (go run, default port: 8083)' \
		'  make build          Build the console binary' \
		'  make test           Run the console tests' \
		'  make docker-build   Build the local Docker image' \
		'  make docker-run     Build and run with Docker' \
		'  make docker         Alias for docker-run' \
		'' \
		'Override defaults with PORT=<port> or IMAGE=<image>.'

run:
	CONSOLE_PORT=$(PORT) go run .

build:
	CGO_ENABLED=0 go build -o ./tmp/console .

test:
	go test ./...

docker-build:
	docker build -t "$(IMAGE)" .

docker-run: docker-build
	docker run --rm -p "127.0.0.1:$(PORT):8083" "$(IMAGE)"

docker: docker-run
