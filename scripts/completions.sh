#!/bin/sh
# Generates shell completions for the release archives (run by GoReleaser).
# The command is fritzbox; fb is a symlink to it, so every script also completes fb.
set -e
cd "$(dirname "$0")/.."
rm -rf completions
mkdir completions
go build -o completions/fritzbox ./cmd/fritzbox
completions/fritzbox completion bash |
	sed 's/-F __start_fritzbox fritzbox$/-F __start_fritzbox fritzbox fb/' > completions/fritzbox.bash
# bash-completion loads completions by command name, so fb needs its own file.
cp completions/fritzbox.bash completions/fb.bash
completions/fritzbox completion zsh |
	sed -e '1s/^#compdef fritzbox$/#compdef fritzbox fb/' \
		-e 's/^compdef _fritzbox fritzbox$/compdef _fritzbox fritzbox fb/' > completions/_fritzbox
completions/fritzbox completion fish > completions/fritzbox.fish
echo 'complete -c fb -w fritzbox' > completions/fb.fish
rm completions/fritzbox
# Fail the release if a cobra update changed the lines patched above.
test "$(grep -c -- '-F __start_fritzbox fritzbox fb$' completions/fritzbox.bash)" = 2
grep -q '^#compdef fritzbox fb$' completions/_fritzbox
grep -q '^compdef _fritzbox fritzbox fb$' completions/_fritzbox
