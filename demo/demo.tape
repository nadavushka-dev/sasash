# Demo tape for the sasash TUI. Run from the repo root via `make demo`.
# Drives the BUILT binary (./sasash) against the RFC 5737 fixture in demo/home.

Output demo/out/demo.gif
Output demo/out/demo.txt

Set Shell "bash"
Set FontSize 16
Set Width 900
Set Height 500
Set TypingSpeed 40ms
Set Padding 10

Hide
Type "HOME=$PWD/demo/home ./sasash"
Enter
Sleep 1.5s
Show

Sleep 1s
Screenshot demo/out/list.png
Type "j"
Sleep 1s
Screenshot demo/out/selected-db.png
Type "k"
Sleep 500ms
Type "q"
Sleep 500ms
