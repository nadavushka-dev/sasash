# SASASH.4.28 demo — a wider gap between the host list and the detail pane.
# Drives the BUILT ./sasash against the RFC 5737 fixture in demo/home;
# SASASH_BIN overrides the binary so the same tape renders a before shot
# from main (gap 2) beside the after shot (gap 6).
#
# Never press a spawn key (enter on a host, H): inside a multiplexer those open
# REAL tabs on the recorder. This tape only toggles and navigates.

Output demo/out/demo-4.28.gif

Set Shell "bash"
Set FontSize 16
Set Width 1000
Set Height 560
Set TypingSpeed 45ms
Set Padding 10

Hide
Type `mkdir -p demo/out; rm -rf demo/out/home; cp -R demo/home demo/out/home; HOME="$PWD/demo/out/home" "${SASASH_BIN:-./sasash}"`
Enter
Sleep 1.5s
Show

# Default: the managed host beside its paths — the gap is the whole
# separation between the two columns.
Sleep 2s
Screenshot demo/out/frame-4.28-default.png
Sleep 1s

# `.` reveals the ambient tier so the list column has more rows to read
# against the detail pane.
Type "."
Sleep 1.3s
Screenshot demo/out/frame-4.28-ambient.png

# `$` shows the command line; j/k walk the list — the gap holds on every row.
Type "$"
Sleep 700ms
Type "j"
Sleep 900ms
Type "j"
Sleep 900ms
Screenshot demo/out/frame-4.28-walk.png
Type "k"
Sleep 600ms
Type "k"
Sleep 1.2s

Hide
Type "q"
Sleep 500ms
