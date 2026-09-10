# SASASH.4.33 demo — C: copy files with scp, both ends browsed.
# Drives the BUILT ./sasash against the RFC 5737 fixture in demo/home.
#
# The recorder has no sshd to reach, so demo/bin/ssh and demo/bin/scp stand
# in on PATH: the `ssh <alias> ls -1Ap -- <path>` listing runs the same ls
# against demo/remote (copied to demo/out/remote), and scp copies between the
# fixture home and that tree with a progress line. The argv sasash builds is
# the real one (the confirm popup and the $ line show it); only where it
# lands is faked. Never put demo/bin on a real PATH.
#
# Never press a spawn key (enter on a host, H): inside a multiplexer those open
# REAL tabs on the recorder. C's enter runs the scp stand-in in the foreground.

Output demo/out/demo-4.33-scp.gif
Output demo/out/demo-4.33-scp.txt

Set Shell "bash"
Set FontSize 16
Set Width 1100
Set Height 640
Set TypingSpeed 45ms
Set Padding 10

Hide
Type `mkdir -p demo/out; rm -rf demo/out/home demo/out/remote; cp -R demo/home demo/out/home; cp -R demo/remote demo/out/remote; clear; PATH="$PWD/demo/bin:$PATH" SASASH_DEMO_REMOTE="$PWD/demo/out/remote" HOME="$PWD/demo/out/home" "${SASASH_BIN:-./sasash}"`
Enter
Sleep 1.5s
Show

# api is selected. C opens the copy flow: the direction first.
Sleep 1.5s
Type "C"
Sleep 1.5s
Screenshot demo/out/frame-4.33-direction.png

# u: upload. The local picker opens in $HOME — proj/ is a directory (the d
# of its mode is the glyph), the $ line shows the scp with placeholders.
Type "u"
Sleep 1.5s
Screenshot demo/out/frame-4.33-local-source.png

# enter on proj/ picks it: the source is a directory, so the line says -r.
# The remote browser opens at api:. (the login home) for the dest.
Enter
Sleep 1.5s
Screenshot demo/out/frame-4.33-remote-dest.png

# j to docs/, l opens it, h comes back up; . picks the directory we are in.
Type "j"
Sleep 600ms
Type "l"
Sleep 1.2s
Type "h"
Sleep 1.2s
Type "."
Sleep 1.5s
Screenshot demo/out/frame-4.33-confirm-upload.png

# enter hands the terminal to scp: its own progress, then back with the notice.
Enter
Sleep 3s
Screenshot demo/out/frame-4.33-uploaded.png
Sleep 1s

# Download: C, d — the remote browser first. j j to logs/, l into it, j onto
# app.log (the cursor lands on ../ after a descent), enter picks the file.
Type "C"
Sleep 800ms
Type "d"
Sleep 1.5s
Screenshot demo/out/frame-4.33-remote-source.png
Type "j"
Sleep 500ms
Type "j"
Sleep 500ms
Type "l"
Sleep 1.2s
Type "j"
Sleep 800ms
Screenshot demo/out/frame-4.33-remote-file.png
Enter
Sleep 1.5s
Screenshot demo/out/frame-4.33-local-dest.png

# . picks the home as the dest; the confirm names the exact scp.
Type "."
Sleep 1.5s
Screenshot demo/out/frame-4.33-confirm-download.png
Enter
Sleep 3s
Screenshot demo/out/frame-4.33-downloaded.png
Sleep 1.5s

Hide
Type "q"
Sleep 500ms
