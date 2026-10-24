.PHONY: help build test migration-check clean run-ssp run-dsp run-dmp infra-up infra-down

help:
	@printf '%s\n' \
		'Open Ads Build & Run targets:' \
		'  make build       Build all three Go applications (ssp, dsp, dmp) into bin/' \
		'  make test        Run all unit tests across ssp, dsp, and dmp' \
		'  make migration-check Validate migration files and compose wiring' \
		'  make run-ssp     Run SSP application locally (port 8080)' \
		'  make run-dsp     Run DSP application locally (port 8081)' \
		'  make run-dmp     Run DMP application locally (port 8082)' \
		'  make infra-up    Start local development infrastructure (MariaDB, Valkey, ClickHouse)' \
		'  make infra-down  Stop local development infrastructure' \
		'  make clean       Remove compiled binaries'

build:
	@mkdir -p bin
	@echo "==> Building SSP..."
	cd ssp && go build -o ../bin/ssp ./cmd/ssp
	@echo "==> Building DSP..."
	cd dsp && go build -o ../bin/dsp ./cmd/dsp
	@echo "==> Building DMP..."
	cd dmp && go build -o ../bin/dmp ./cmd/dmp
	@echo "==> All applications built successfully in bin/"

test:
	@echo "==> Testing SSP..."
	cd ssp && go test -v -race ./...
	@echo "==> Testing DSP..."
	cd dsp && go test -v -race ./...
	@echo "==> Testing DMP..."
	cd dmp && go test -v -race ./...
	@echo "==> All tests passed!"

migration-check:
	@docker compose -f deployments/docker-compose.yml config >/dev/null
	@test -s ssp/migrations/000001_init_ssp_schema.up.sql
	@test -s ssp/migrations/000001_init_ssp_schema.down.sql
	@test -s dsp/migrations/000001_init_dsp_schema.up.sql
	@test -s dsp/migrations/000001_init_dsp_schema.down.sql
	@test -s dmp/migrations/000001_init_dmp_schema.up.sql
	@test -s dmp/migrations/000001_init_dmp_schema.down.sql
	@echo "==> Migration files and compose wiring are valid"

run-ssp:
	cd ssp && go run ./cmd/ssp

run-dsp:
	cd dsp && go run ./cmd/dsp

run-dmp:
	cd dmp && go run ./cmd/dmp

infra-up:
	docker compose -f deployments/docker-compose.yml up -d

infra-down:
	docker compose -f deployments/docker-compose.yml down

clean:
	rm -rf bin ssp/bin dsp/bin dmp/bin
