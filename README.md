# sasash

A terminal UI over your `ssh_config`: hosts, live path status, and one keypress to
connect. sasash never does anything you could not see or do yourself — see `SPEC.md`
for the design of record.

![sasash demo](demo/demo.gif)

The demo drives the real binary against the fixture config in `demo/home`
(RFC 5737 documentation addresses, so every path shows as down). Regenerate it with
`make demo` (requires [vhs](https://github.com/charmbracelet/vhs): `brew install vhs`;
the target skips gracefully without it). A re-render can differ by a frame from
timing jitter even with no content change, so only commit the regenerated
`demo/demo.gif` when the demo content actually changed.
