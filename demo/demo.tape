# Demo tape for the sasash TUI. Run from the repo root via `make demo`.
# Drives the BUILT binary (./sasash) against the RFC 5737 fixture in demo/home.
#
# NOTE: vhs resolves Screenshot by frame index into the recorded frame stream,
# so which frame lands in a PNG is approximate — hence the neutral frame-* names.
# The gif and the demo.txt frame dump are the trustworthy artifacts.
#
# Never press a key that spawns (enter on a host, H): the render runs in
# whatever terminal renders it, and inside herdr or tmux those open REAL
# tabs/windows on the recorder's multiplexer (it happened once).

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
Type "o"
Sleep 1s
Screenshot demo/out/frame-settings.png
Type "/"
Type "th"
Sleep 1s
Screenshot demo/out/frame-search.png
Escape
Sleep 300ms
Escape
Sleep 500ms
Type "t"
Sleep 500ms
Type "o"
Sleep 1s
Screenshot demo/out/frame-settings-mocha.png
Type "jj"
Sleep 500ms
Enter
Sleep 1s
Screenshot demo/out/frame-e.png
Escape
Sleep 300ms
Escape
Sleep 300ms
Type "ttt"
Sleep 500ms
Type "l"
Sleep 800ms
Screenshot demo/out/frame-paths.png
Type "j"
Sleep 500ms
Type "l"
Sleep 800ms
Screenshot demo/out/frame-tunnels.png
Escape
Sleep 300ms
Type "?"
Sleep 1.5s
Screenshot demo/out/frame-keys.png
Escape
Sleep 500ms
Hide
Type "q"
Sleep 500ms
