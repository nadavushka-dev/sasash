# SASASH.4.27 demo — multi-spec forward input. A forward prompt accepts multiple
# whitespace-separated specs -> one set with N forwards; each token expands via
# the 4.9 shorthand, the preview shows every expanded spec, all-or-nothing.
# Drives the BUILT ./sasash against the RFC 5737 fixture in demo/home.
#
# Never press a spawn key (enter on a host, H). The `+` step is cancelled before
# it connects (the fixture host is not real); `n` (declare-only) then renders a
# real multi-spec set with no spawn wait.

Output demo/out/demo-4.27.gif

Set Shell "bash"
Set FontSize 16
Set Width 1000
Set Height 560
Set TypingSpeed 55ms
Set Padding 10

Hide
Type `mkdir -p demo/out; rm -rf demo/out/home; cp -R demo/home demo/out/home; HOME="$PWD/demo/out/home" ./sasash`
Enter
Sleep 1.5s
Show

# `+` on the managed host: type three bare ports separated by spaces. The
# shorthand expands each, and the preview line under the prompt shows the
# whole set enter would create (matches the human's `forward … 4011 4012 4013`).
Sleep 1s
Type "+"
Sleep 700ms
Type "4011 4012 4013"
Sleep 1.4s
Screenshot demo/out/frame-4.27-multispec.png
# Cancel here — connecting to the fixture host would just fail; the point is
# the multi-spec input and its live expansion.
Escape
Sleep 700ms

# `n` declares the same three-forward set (desire only, nothing spawned) so it
# renders immediately: name the set, then the specs with their expansion.
Type "n"
Sleep 700ms
Type "svc"
Enter
Sleep 700ms
Type "4011 4012 4013"
Sleep 1.3s
Screenshot demo/out/frame-4.27-specs.png
Enter
Sleep 1.5s
# The set is one row per forward under the tunnels section — all down (declared,
# not started; space would connect the set).
Screenshot demo/out/frame-4.27-set.png
Sleep 800ms

Hide
Type "q"
Sleep 500ms
