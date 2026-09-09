# SASASH.4.29 — an ultrawide terminal: the frame is capped and centered,
# the panes sit together mid-screen, the top stays the top.

Output demo/out/demo-4.29-wide.gif
Output demo/out/demo-4.29-wide.txt

Set Shell "bash"
Set FontSize 14
Set Width 2200
Set Height 760
Set TypingSpeed 40ms
Set Padding 10

Hide
Type `mkdir -p demo/out; rm -rf demo/out/home; cp -R demo/home demo/out/home; python3 -c "import socket,time;s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind((\"127.0.0.1\",2222));s.listen();time.sleep(600)" & sleep 0.5; clear; HOME="$PWD/demo/out/home" "${SASASH_BIN:-./sasash}"; kill %1 2>/dev/null`
Enter
Sleep 2s
Show
Sleep 2.5s
Screenshot demo/out/frame-wide.png
Type "j"
Sleep 1s
Type "."
Sleep 800ms
Type "$"
Sleep 1.2s
Screenshot demo/out/frame-wide-toggles.png
Hide
Type "q"
Sleep 400ms
