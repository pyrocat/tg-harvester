.PHONY: run build test lint tidy

run:
	go run ./cmd/tg-harvester

build:
	CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o bin/tg-harvester ./cmd/tg-harvester

test:
	go test -race ./...

lint:
	go vet ./...
	@test -z "$$(gofmt -l .)" || (gofmt -l . && exit 1)

tidy:
	go mod tidy
