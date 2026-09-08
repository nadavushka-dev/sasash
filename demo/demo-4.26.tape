# SASASH.4.26 demo — cleaner host detail: ambient tier and the command-preview
# footer are both HIDDEN BY DEFAULT, each with a toggle. Drives the BUILT
# ./sasash against the RFC 5737 fixture in demo/home.
#
# Never press a spawn key (enter on a host, H): inside a multiplexer those open
# REAL tabs on the recorder. This tape only toggles and navigates.

Output demo/out/demo-4.26.gif

Set Shell "bash"
Set FontSize 16
Set Width 1000
Set Height 560
Set TypingSpeed 45ms
Set Padding 10

Hide
Type `mkdir -p demo/out; rm -rf demo/out/home; cp -R demo/home demo/out/home; HOME="$PWD/demo/out/home" ./sasash`
Enter
Sleep 1.5s
Show

# Default: only the managed host (api) shows; web/db are ambient and hidden,
# and there is no permanent command-preview footer line. A clean detail.
Sleep 2s
Screenshot demo/out/frame-4.26-default.png
Sleep 1.5s

# `.` reveals the ambient tier: web and db appear under the divider.
Type "."
Sleep 1.3s
Screenshot demo/out/frame-4.26-ambient.png
# `.` hides it again — back to the clean list.
Type "."
Sleep 900ms

# `$` reveals the command-preview footer. The toggle-on notice takes the line
# first; k clears it and the `$ ssh api` line appears.
Type "$"
Sleep 700ms
Type "k"
Sleep 1.2s
Screenshot demo/out/frame-4.26-preview.png

# Focus the paths pane (l): the last-connected / last-switch line now sits
# under the paths as host history, ABOVE the tunnels section — not inside it.
Type "l"
Sleep 1.4s
Screenshot demo/out/frame-4.26-lastseen.png
Escape
Sleep 400ms

Hide
Type "q"
Sleep 500ms
