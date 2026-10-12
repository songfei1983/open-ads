PORT ?= 4173
IMAGE ?= open-ads-prototype:local

.PHONY: help run python docker-build docker-run docker

help:
	@printf '%s\n' \
		'Open Ads prototype targets:' \
		'  make run            Serve locally with Python (default port: 4173)' \
		'  make python         Alias for run' \
		'  make docker-build   Build the local Docker image' \
		'  make docker-run     Build and serve with Docker' \
		'  make docker         Alias for docker-run' \
		'' \
		'Override defaults with PORT=<port> or IMAGE=<image>.'

run:
	python3 -m http.server "$(PORT)" --bind 127.0.0.1 --directory .

python: run

docker-build:
	docker build -t "$(IMAGE)" .

docker-run: docker-build
	docker run --rm -p "127.0.0.1:$(PORT):4173" "$(IMAGE)"

docker: docker-run
