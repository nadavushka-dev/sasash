# CLAUDE.md — sasash

Go TUI over `ssh_config` and OpenSSH control sockets. **`SPEC.md` is the design of
record** — read §2 (source of truth), §3 (probe, never guess), §4 (exact ssh argv),
§9 (milestones), §12 (testing) before writing code. Where code and SPEC drift, the
**code wins** — fix the spec wording in the same branch instead of "correcting" the code.

## Principle

sasash never does anything the user could not see or do themselves. It is a *view*
over `ssh_config` — not an SSH implementation, not a daemon, not a second place
connection info lives.

- Shell out to `/usr/bin/ssh`. **Never use `golang.org/x/crypto/ssh`** (it ignores
  ssh_config, ssh-agent, ProxyJump, ControlMaster — everything this design is built on).
- No background daemon, no auto-reconnect, no ProxyCommand dialer, no secret storage.
- sasash writes exactly one config file (`~/.ssh/config.d/sasash.conf`) plus a single
  one-time confirmed `Include` insertion into `~/.ssh/config`.

## Stack and layout

Dependencies are capped at `bubbletea`, `bubbles`, `lipgloss` (SPEC §8). Adding any
other dependency requires explicit approval.

| package | rule |
|---|---|
| `cmd/sasash` | stdlib subcommand dispatch; each subcommand self-registers from its OWN file — adding a subcommand must not edit `main.go` |
| `internal/run` | the ONLY importer of `os/exec`; every process goes through `run.Runner` |
| `internal/probe` | the only network dialing; paths are opaque data |
| `internal/sshconf` | lossless parser + atomic writer; do NOT edit `sshconf.go` — extend the package with new files |
| `internal/hostmodel` | domain model over sshconf meta; no I/O beyond config reads, no dialing |
| `internal/setup` | one-time bootstrap; pure check/ensure functions over a home-dir parameter |
| `internal/ui` | Bubbletea models; smoke-tested only |

## The seams

All outside-world access goes through injectable seams: `run.Runner` (processes),
`probe.Dialer` (network), and a clock where elapsed time matters. Nothing else touches
the outside world — that is what makes the logic testable without a live SSH server.

- A non-zero exit is **data** (`run.Result.Code`), not an error. `Run` errors only when
  the process could not run at all.
- `probe.Select` = first alive path in **config order** (order is preference, not
  latency). `Fastest` exists separately for when speed is the question.

## Invariants (violating these is a bug, not a style choice)

- **sshconf is lossless by construction**: every line keeps its own terminator;
  rendering an untouched file returns it byte-for-byte. Edits rewrite only the lines
  they touch, preserving indent and keyword casing.
- **All config writes**: temp file + fsync + rename (`WriteAtomic`). Never truncate in
  place — this code writes files the user also hand-edits. Re-read before writing;
  hand-edits win.
- **Read host meta via `OwnMeta`/`OwnMetaVerb`**, never `Meta`/`MetaVerb` — the raw
  variants bleed the next host's leading comment block into the current host.
- **Never infer location** from SSID, gateway, or `utun*` interfaces. Only a TCP
  connect is trusted.
- **Tunnel state: reality wins.** Report three distinct states (`up` / `listening` /
  `down`) from `-O check` + a local port dial; the state file is a cache, never truth.

## Testing

Gate for every merge: `make check` (vet + race + coverage) green, `make build` green,
`gofmt -l .` clean. Run `make fuzz` when touching `internal/sshconf`.

Conventions (match exactly):
- External test packages (`package x_test`); exported API only.
- `t.Parallel()` in every test AND every subtest closure.
- Names `TestType_Method_Case`; table tests as anonymous struct slices, `name` field
  first; `t.Errorf("Method() = %v, want %v", got, want)`; no assertion libraries.
- Filesystem is never faked — `t.TempDir()` + real files.
- Fakes: `fakerun` (stubs keyed on argv joined with spaces; unstubbed calls fail loudly)
  and `fakedial` (keyed on full `host:port`).
- Coverage targets: `sshconf` ~100%, logic packages ≥95%, UI smoke-only — **no golden
  `View()` strings** (they break on cosmetic changes and catch nothing).
- New `internal/sshconf/testdata/*.conf` fixtures automatically join the round-trip AND
  fuzz corpora — every fixture must round-trip byte-exactly.
- Argv construction is table-tested to exact strings — these strings are the product;
  a typo in `-O cancel` silently does nothing.

## UI rules

- Vim-like keybindings are mandatory (j/k, g/G, ctrl+d/u, `/`, esc) alongside the
  SPEC §5 action keys; single-letter actions must not fire while the filter is focused.
- Dark themes only; gruvbox is the default. Every lipgloss style derives from the theme
  palette struct — **no color literal outside the palette layer**.
- Pair every color signal with a glyph (`●`/`◐`/`○`) so status survives `NO_COLOR`.

## Public repo — data hygiene

Never commit real infrastructure addresses, hostnames, or usernames. Use RFC 5737
documentation addresses (`192.0.2.x`, `198.51.100.x`, `203.0.113.x`) in examples,
tests, and docs.

## Workflow

- Never commit or merge to `main` unless explicitly instructed. Feature branches:
  `feat/sasash-<id>-<slug>` per tracker slice.
- When multiple agent sessions work this repo, each works in its own `git worktree`;
  the primary checkout stays on `main`.
- Local-only, gitignored, never committed: `.pin-board/` (cross-session task tracker —
  see its README) and `.squad/` (multi-agent run coordination: briefs, contracts,
  status bus), when present.
