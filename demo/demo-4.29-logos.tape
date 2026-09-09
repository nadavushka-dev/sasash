# SASASH.4.29 — the wordmark candidates, one build each: the face is a
# package string var selected at link time (-ldflags -X), so the product
# carries no knob. Rendered at a full-screen size; same fixture as demo.tape.

Output demo/out/demo-4.29-logos.gif
Output demo/out/demo-4.29-logos.txt

Set Shell "bash"
Set FontSize 16
Set Width 1400
Set Height 800
Set TypingSpeed 40ms
Set Padding 10

Hide
Type `mkdir -p demo/out/bin; rm -rf demo/out/home; cp -R demo/home demo/out/home; python3 -c "import socket,time;s=socket.socket();s.setsockopt(socket.SOL_SOCKET,socket.SO_REUSEADDR,1);s.bind((\"127.0.0.1\",2222));s.listen();time.sleep(600)" & sleep 0.5; for f in slant smslant speed; do go build -ldflags "-X github.com/nadavushka-dev/sasash/internal/ui.logoFaceName=$f" -o demo/out/bin/sasash-$f ./cmd/sasash; done; clear`
Enter
Sleep 8s
Type `clear; HOME="$PWD/demo/out/home" demo/out/bin/sasash-slant`
Enter
Sleep 2s
Show
Sleep 2.5s
Screenshot demo/out/frame-logo-slant.png
Hide
Type "q"
Sleep 400ms
Type `clear; HOME="$PWD/demo/out/home" demo/out/bin/sasash-smslant`
Enter
Sleep 2s
Show
Sleep 2.5s
Screenshot demo/out/frame-logo-smslant.png
Hide
Type "q"
Sleep 400ms
Type `clear; HOME="$PWD/demo/out/home" demo/out/bin/sasash-speed`
Enter
Sleep 2s
Show
Sleep 2.5s
Screenshot demo/out/frame-logo-speed.png
Hide
Type "q"
Sleep 400ms
