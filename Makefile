.PHONY: build test lint
build:
	go build -ldflags "-s -w" -o bin/fb ./cmd/fb
test:
	go test -race ./...
lint:
	go vet ./...
