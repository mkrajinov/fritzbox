#!/bin/sh
# Generates shell completions for the release archives (run by GoReleaser).
set -e
rm -rf completions
mkdir completions
for sh in bash zsh fish; do
	go run ./cmd/fb completion "$sh" > "completions/fb.$sh"
done
