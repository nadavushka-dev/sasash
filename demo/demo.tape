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
Type `mkdir -p demo/out; rm -rf demo/out/home; cp -R demo/home demo/out/home; HOME="$PWD/demo/out/home" ./sasash`
Enter
Sleep 1.5s
Show

Sleep 1s
# SASASH.4.26 — the clean default: ambient "other ssh_config entries" and the
# command-preview footer are both hidden. This is the first thing seen.
Screenshot demo/out/frame-a.png
# `.` reveals the ambient tier (web/db under the divider); `$` reveals the
# command-preview footer. The rest of the demo runs with both on.
Type "."
Sleep 800ms
Type "$"
Sleep 800ms
Screenshot demo/out/frame-toggles.png
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
# SASASH.4.23 — one focus model; a focused section only moves the highlight,
# it never hides the other. api shows paths AND tunnels together (list focus).
Screenshot demo/out/frame-nav-list.png
Sleep 700ms
# l cycles list -> paths: the mark lands on a path row, tunnels still below.
Type "l"
Sleep 900ms
Screenshot demo/out/frame-paths.png
# move the mark to vpn and switch the active path — enter writes HostName.
Type "j"
Sleep 700ms
Enter
Sleep 1.2s
Screenshot demo/out/frame-switch.png
Sleep 600ms
# l again to paths, l once more to tunnels: paths stay visible above.
Type "l"
Sleep 400ms
Type "l"
Sleep 900ms
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
