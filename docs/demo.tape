# Records docs/demo.gif against the fake box from internal/fakebox (no real router, no real data).
#
#   brew install vhs
#   vhs docs/demo.tape          # run from the repository root

Output docs/demo.gif

Set Shell bash
Set FontSize 15
Set Width 1280
Set Height 600
Set Padding 16
Set Theme "Catppuccin Mocha"
Set TypingSpeed 45ms

Hide
Type "make build >/dev/null && export PATH=$PWD/bin:$PATH PS1='$ '" Enter
Type "go run ./internal/fakebox/fakeboxd -name home -env bin/demo.env >/dev/null 2>&1 & sleep 4; source bin/demo.env; clear" Enter
Sleep 5s
Show

Type "fb status" Sleep 400ms Enter
Sleep 2.5s
Type "fb hosts --active" Sleep 400ms Enter
Sleep 2.5s
Type "fb calls --days 7" Sleep 400ms Enter
Sleep 2.5s
Type "clear; fb tui" Sleep 400ms Enter
Sleep 3s
Tab Sleep 2s
Tab Sleep 2s
Tab Sleep 2s
Tab Sleep 2s
Tab Sleep 2s
Tab Sleep 2s
Type "q"
Sleep 500ms

Hide
Type "kill %1; clear" Enter
