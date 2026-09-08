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

## Install

Prebuilt binaries for macOS and Linux (amd64 and arm64) ship with every
[release](https://github.com/nadavushka-dev/sasash/releases). Onboarding a machine is
two commands, no Go toolchain needed:

```sh
curl -fsSL https://raw.githubusercontent.com/nadavushka-dev/sasash/main/install.sh | sh
sasash setup
```

The installer picks the asset for your OS and architecture, verifies its sha256
against the release's `checksums.txt`, installs to `~/.local/bin` (falling back to
`/usr/local/bin` with sudo), prints a PATH hint if that directory is not on your
PATH, and offers to run `sasash setup` — the one-time bootstrap that adds a single
confirmed `Include` line to `~/.ssh/config`. `SASASH_VERSION=v0.1.0` pins a release;
`sh install.sh --help` lists the other knobs.

Other ways in:

- Homebrew (macOS): `brew install --cask nadavushka-dev/tap/sasash`, once the tap is
  published (see `docs/releasing.md`).
- From source (Go 1.26.4+): `go install github.com/nadavushka-dev/sasash/cmd/sasash@latest`.

`sasash version` prints the release tag the binary was built from. Binaries are not
signed or notarized yet; the Homebrew cask strips the quarantine bit on install, and
a curl|sh install never had it.

![sasash demo](demo/demo.gif)

The demo drives the real binary against the fixture config in `demo/home`
(RFC 5737 documentation addresses, so every path shows as down). Regenerate it with
`make demo` (requires [vhs](https://github.com/charmbracelet/vhs): `brew install vhs`;
the target skips gracefully without it). A re-render can differ by a frame from
timing jitter even with no content change, so only commit the regenerated
`demo/demo.gif` when the demo content actually changed.
