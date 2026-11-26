.PHONY: proto sqlc lint test test-integration build-all tidy

BIN_DIR := bin
SERVICES := aegis-api aegis-scheduler aegis-ingest aegis-ws aegis-dispatcher aegis-agent

proto:
	cd api/proto && buf lint && buf generate

sqlc:
	sqlc generate -f sqlc.yaml

lint:
	golangci-lint run ./...

test:
	go test -race ./...

test-integration:
	go test -race -tags=integration ./...

build-all:
	@mkdir -p $(BIN_DIR)
	@for svc in $(SERVICES); do \
		echo "Building $$svc..."; \
		CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o $(BIN_DIR)/$$svc ./cmd/$$svc; \
	done

tidy:
	go mod tidy
