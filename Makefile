.PHONY: build test lint completions
build:
	go build -ldflags "-s -w" -o bin/fritzbox ./cmd/fritzbox
	ln -sf fritzbox bin/fb
test:
	go test -race ./...
lint:
	go vet ./...
completions:
	./scripts/completions.sh
