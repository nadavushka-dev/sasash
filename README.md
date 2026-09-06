# sasash

A terminal UI over your `ssh_config`: hosts, live path status, and one keypress to
connect. sasash never does anything you could not see or do yourself — see `SPEC.md`
for the design of record.

Runs natively on macOS and Linux; on Windows, use it inside WSL (the Windows OpenSSH
port has no ControlMaster multiplexing, which is the engine everything here rides on).
The `y` yank goes through the terminal's own OSC 52 clipboard escape first — it works over
ssh in terminals that support it (most modern ones; not GNOME Terminal or macOS Terminal, and
tmux swallows it unless `set-clipboard` and the terminal's `Ms` capability let it through) —
with pbcopy / wl-copy / xclip / clip.exe behind it, and it says which one took the text.

![sasash demo](demo/demo.gif)

The demo drives the real binary against the fixture config in `demo/home`
(RFC 5737 documentation addresses, so every path shows as down). Regenerate it with
`make demo` (requires [vhs](https://github.com/charmbracelet/vhs): `brew install vhs`;
the target skips gracefully without it). A re-render can differ by a frame from
timing jitter even with no content change, so only commit the regenerated
`demo/demo.gif` when the demo content actually changed.
