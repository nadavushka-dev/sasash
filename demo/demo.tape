# Demo tape for the sasash TUI. Run from the repo root via `make demo`.
# Drives the BUILT binary (./sasash) against the RFC 5737 fixture in demo/home.
# SASASH_BIN overrides the binary so the same tape renders a before shot.
#
# NOTE: vhs resolves Screenshot by frame index into the recorded frame stream,
# so which frame lands in a PNG is approximate — hence the neutral frame-* names.
# The gif and the demo.txt frame dump are the trustworthy artifacts.
#
# Never press a key that spawns (enter on a host, H): the render runs in
# whatever terminal renders it, and inside herdr or tmux those open REAL
# tabs/windows on the recorder's multiplexer (it happened once).
#
# A loopback listener on 127.0.0.1:2222 backs the fixture's `bastion` path,
# so one row is genuinely reachable (a real TCP connect, SPEC §3) — the
# other addresses are RFC 5737 and read down.

Output demo/out/demo.gif
Output demo/out/demo.txt

Set Shell "bash"
Set FontSize 16
Set Width 1400
Set Height 800
Set TypingSpeed 40ms
Set Padding 10

Hide
Type `mkdir -p demo/out; rm -rf demo/out/home; cp -R demo/home demo/out/home; python3 -c "import socket,time;s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind((\"127.0.0.1\",2222));s.listen();time.sleep(600)" & sleep 0.5; clear; HOME="$PWD/demo/out/home" "${SASASH_BIN:-./sasash}"; kill %1 2>/dev/null`
Enter
Sleep 2s
Show

Sleep 1.5s
# SASASH.4.29 — the first thing seen: the wordmark, glanceable rows
# (glyph · latency · tunnels up), the frame centered on a wide terminal.
Screenshot demo/out/frame-a.png
Type "j"
Sleep 1s
# bastion: the one reachable row, ● with a real latency, ← active in the pane.
Screenshot demo/out/frame-b.png
Type "j"
Sleep 800ms
Type "j"
Sleep 800ms
Type "g"
Sleep 500ms
# `.` reveals the ambient tier (web/db under the divider); `$` the command line.
Type "."
Sleep 800ms
Type "$"
Sleep 1s
Screenshot demo/out/frame-toggles.png
Type "/"
Type "ba"
Sleep 1s
Screenshot demo/out/frame-search.png
Escape
Sleep 300ms
Escape
Sleep 500ms
Type "o"
Sleep 1s
Screenshot demo/out/frame-settings.png
Escape
Sleep 300ms
Type "t"
Sleep 800ms
Type "t"
Sleep 800ms
Type "t"
Sleep 800ms
Screenshot demo/out/frame-theme.png
Type "t"
Sleep 500ms
# l cycles list -> paths: the mark lands on a path row; j to vpn, enter
# writes HostName — the row's glyph follows the path ssh now dials.
Type "l"
Sleep 900ms
Screenshot demo/out/frame-paths.png
Type "j"
Sleep 700ms
Enter
Sleep 1.2s
Screenshot demo/out/frame-switch.png
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
