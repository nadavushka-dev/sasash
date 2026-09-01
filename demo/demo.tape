# Demo tape for the sasash TUI. Run from the repo root via `make demo`.
# Drives the BUILT binary (./sasash) against the RFC 5737 fixture in demo/home.
#
# NOTE: vhs resolves Screenshot by frame index into the recorded frame stream,
# so which frame lands in a PNG is approximate — hence the neutral frame-* names.
# The gif and the per-frame demo.txt are the trustworthy artifacts.

Output demo/out/demo.gif
Output demo/out/demo.txt

Set Shell "bash"
Set FontSize 16
Set Width 900
Set Height 500
Set TypingSpeed 40ms
Set Padding 10

Hide
Type `HOME="$PWD/demo/home" ./sasash`
Enter
Sleep 1.5s
Show

Sleep 1s
Screenshot demo/out/frame-a.png
Type "j"
Sleep 1s
Screenshot demo/out/frame-b.png
Type "k"
Sleep 500ms
Hide
Type "q"
Sleep 500ms
