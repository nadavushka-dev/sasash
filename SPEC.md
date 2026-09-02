# sasash — spec v0.1

A TUI for SSH hosts and tunnels. Go + Bubbletea.

**Principle: sasash never does anything you can't see or do yourself.**
It is a *view* over `ssh_config` and OpenSSH control sockets — not an SSH implementation,
not a daemon, not a second place your connection info lives.

---

## 1. Scope

### Solves
1. Remembering which host alias exists → searchable list.
2. Getting an address as *data* for non-ssh uses → copy to clipboard / `sasash addr <host>`.
3. Adding a port-forward without killing the existing tunnel → `ssh -O forward`.
4. Bonus, and the one that bites daily: **knowing which network path to a host is alive right now.**

### Explicitly out of scope
- No background daemon, no launchd, no auto-reconnect. Dead tunnel goes red; you press `r`.
- No `ProxyCommand` auto-dialer. sasash must never sit in the path of every ssh connection.
- No embedded terminal / pane multiplexing. `exec` out and let herdr or tmux do that job.
- No secret storage, ever. Auth is `ssh-agent`'s problem.
- No per-tool integrations. Non-ssh tools are command templates (§6).

### Non-negotiable implementation rule
**Shell out to `/usr/bin/ssh`. Never use `golang.org/x/crypto/ssh`.**
The Go SSH library ignores `ssh_config`, `ssh-agent`, `ProxyJump`, and `ControlMaster` —
i.e. everything this design is built on. sasash constructs argv and execs. That's it.

---

## 2. Source of truth

`~/.ssh/config.d/sasash.conf`, pulled in by one line at the **top** of `~/.ssh/config`
(ssh_config is first-value-wins, so managed entries must precede any `Host *` defaults):

```sshconfig
Include ~/.ssh/config.d/sasash.conf
```

sasash adds that line once, with confirmation, and never touches `~/.ssh/config` again.

- sasash **parses** every ssh config file, to *display* all hosts.
- sasash **writes** exactly one file: `sasash.conf`.

Everything downstream — `ssh`, `herdr --remote`, `scp`, `rsync`, `git`, VS Code Remote —
reads this natively. Verified: herdr's `[remote] manage_ssh_config = true` generates a config
that "includes your `~/.ssh/config` first", so herdr resolves sasash aliases with zero integration.

### Format

Metadata rides in `# @sasash` comments. `ssh` ignores them; sasash reads them. One file, no drift.

```sshconfig
# ─── managed by sasash. hand-editable; unknown lines are preserved verbatim. ───

# @sasash path lan 192.168.3.141
# @sasash path ts  100.105.178.123
# @sasash tags dev,tlv
# @sasash forwards web  3000:localhost:3000 3001:localhost:3001
# @sasash forwards api  4011:localhost:4011 4012:localhost:4012 4013:localhost:4013
Host ubuntu
  HostName 192.168.3.141
  User nadav
  ControlMaster auto
  ControlPath ~/.ssh/cm/%C
  ControlPersist 30m

# pinned variants, generated — for when you want to force one path
Host ubuntu.lan
  HostName 192.168.3.141
  User nadav
Host ubuntu.ts
  HostName 100.105.178.123
  User nadav
```

**`ControlPath ~/.ssh/cm/%C`** — not `%r@%h:%p`. macOS caps unix socket paths at 104 bytes and
the readable form overflows on long hostnames. `%C` is a hash. `mkdir -p ~/.ssh/cm` on first run.

### Parser requirement
Line-oriented, round-trip faithful: every line sasash doesn't understand is retained byte-for-byte,
including blank lines, comments, and directives sasash has never heard of. Rewrite only the lines
that changed. ~200 lines of Go — write it rather than depending on a library whose round-trip
fidelity you'd have to audit anyway.

If the user hand-edits the file, sasash re-reads it and does not fight.

---

## 3. Paths: probe, never guess

A host has an ordered list of named addresses. Which one works changes hourly, and sasash does
not care why: a VPN dropped, an overlay network went quiet, the laptop moved, the host rebooted.
Paths are opaque data. sasash probes them and reports; it never reasons about what a path *is*.

**Do not infer location.** SSID, default gateway, and `utun*` interfaces all lie
(three utun interfaces can be up simultaneously and none of them identify themselves).

On refresh, dial every path in parallel:

```go
net.DialTimeout("tcp", net.JoinHostPort(addr, port), 1*time.Second)
```

Measured on this machine: LAN answered in 20ms, public in 70ms, Tailscale not at all.
Full probe of 3 paths ≈ 100ms worst case. Render `● lan 20ms · ○ ts down`.

Switching path = rewrite `HostName` in `sasash.conf`. Explicit, instant, sticky, and
every other ssh-aware tool on the machine follows immediately.

---

## 4. Tunnels

### Model
- **Desired state** → `# @sasash forwards <name> ...` in `sasash.conf`. Durable, named sets.
- **Actual state** → `~/.local/state/sasash/`. A **cache, never truth.**

### Commands (the entire tunnel engine)

| action | command |
|---|---|
| start master + declared forwards | `ssh -fN <host>` |
| is it alive | `ssh -O check <host>` |
| add a forward, live | `ssh -O forward -L 4014:localhost:4014 <host>` |
| remove a forward, live | `ssh -O cancel -L 4014:localhost:4014 <host>` |
| tear down | `ssh -O exit <host>` |

This is why "add port 4014 without killing the other five" is cheap: OpenSSH already does it.
(Note: the `~C` escape does the same thing interactively, but requires a pty — it is inert
under `ssh -N`, which is why it appeared broken.)

### The thing that will bite you
**OpenSSH cannot list active forwards.** `-O check` only reports that the master is alive.
So sasash tracks what it injected — and that record *will* drift the moment anything dies
outside the TUI.

Therefore, on **every** render:
1. `ssh -O check <host>` → master alive?
2. `net.DialTimeout` to `127.0.0.1:<localport>` for each forward → listener present?

Reality wins over the state file, always. Report three distinct states, don't collapse them:

- `● up` — master alive *and* local port accepting
- `◐ listening` — port accepting but not verified end-to-end
- `○ down` — anything else

A tunnel manager that lies about what's up is worse than no tunnel manager.

### Port conflicts
Before adding a forward, dial `127.0.0.1:<port>`. If something already holds it, say so and
offer the next free port. Never silently remap.

---

## 5. TUI

Two panes. Left: filterable host list. Right: detail for the selected host.

```
 sasash                                    /  filter   ?  help

 › ubuntu          paths  ● lan  192.168.3.141   20ms   ← active
   spp                    ○ ts   100.105.178.123 down
   bastion
                   tunnels
                    ● web   3000, 3001              up 12m
                    ○ api   4011, 4012, 4013        down
                    ● ad-hoc 4014                   up 2m

                   $ ssh -fN ubuntu
```

The command line at the bottom always shows what the highlighted action will run, except
while a transient notice (an action's outcome, ssh's own message) holds the line — the
next keypress clears it. On the host row it also names `H`'s command beside `enter`'s
when both fit; `c` copies the `$` command (the highlighted action's), never the `H` one.

| key | action |
|---|---|
| `enter` | ssh to host |
| `H` | `herdr --remote <host>` |
| `y` | yank active host to clipboard (bare address, `sasash addr` parity; the pane shows `host:port`) |
| `p` | cycle path (rewrites `HostName`) |
| `l` / `tab` | focus the tunnels pane: `j`/`k` (and `g`/`G`, `tab`/`shift+tab`) then move the forward highlight and the bottom line previews `space` for it; `h` / `esc` return to the host list. The focused pane's selection is lit, the other's dim |
| `space` | toggle highlighted forward set |
| `+` | add ad-hoc forward (`-O forward`) |
| `x` | cancel highlighted forward (`-O cancel`) |
| `r` | restart master |
| `X` | `ssh -O exit` |
| `e` | open `sasash.conf` in `$EDITOR`, reload on exit |
| `c` | copy the displayed `$` command instead of running it |
| `t` | cycle theme (choice persists) |
| `o` | settings: create config file (the setup flow, confirming exactly what is written), edit config file (`e`), theme picker, add new connection (save first, then offer to connect), add new tunnel (a durable `# @sasash forwards` line — desire only, nothing is started). vim keys navigate; action keys are inert while it is open; `esc` backs out |
| `a` | ad-hoc connect: `[user@]host[:port] [-L spec]…`, offer to save on return |
| `/` `?` `q` | filter, help, quit |

`e` and `t` keep working but are not listed in `?` — settings (`o`) is their discoverable home.

The tunnels section renders the engine's three states verbatim, with one presentation
cap: when two forwards claim the same local port, only the first claimant (declared sets
first, in meta order, then remembered ad-hoc ones) can be the one answering, so every
later claimant shows as `◐ listening` with an "unverified" note even when the engine
reports it up — a stale remembered spec cannot ride a live one's listener.

**Handoff:** use `tea.ExecProcess` to suspend the TUI and give the terminal fully to `ssh`.
Native resize, mouse, colors, `~.`, nested tmux — all free. On exit you're back in the list.

### Keybindings & themes (ratified addendum)

Vim-style navigation is required alongside the action keys above: `j`/`k` (and arrows)
move, `g`/`G` jump to first/last host, `ctrl+d`/`ctrl+u` half-page, `esc` goes back /
clears the filter. Every binding is a named bubbles `key.Binding` so the help view is
generated from the bindings, never hand-maintained. Single-letter action keys must not
fire while the filter input or a prompt is focused.

The UI is dark-only. Every lipgloss style derives from a semantic palette struct — no
hex literal outside the palette layer. Four shipped themes: gruvbox dark (default),
catppuccin mocha, tokyo night, nord; theme selection (flag/env/config) persists and
degrades cleanly to 256-color and `NO_COLOR`. Custom user themes are deferred.

---

## 6. Actions (non-ssh tools)

Tools that don't read ssh_config get a template. No plugin system, no code per tool.

```toml
# ~/.config/sasash/actions.toml
[rdp]
cmd = "xfreerdp /u:{user} /v:{addr} /dynamic-resolution /cert:ignore"
env = { WLR_NO_HARDWARE_CURSORS = "1" }
```

Substitutions: `{host}` `{user}` `{addr}` `{port}`. sasash never learns what the tool is.

`env` values pass to the child verbatim — sasash never shell-evaluates them; if a
value needs evaluation, write it explicitly into `cmd`, e.g.
`cmd = "sh -c 'DISPLAY=$(launchctl getenv DISPLAY) xfreerdp /v:{addr}'"`.

`herdr` needs no entry — it reads `~/.ssh/config`, so `herdr --remote ubuntu` just works.

---

## 7. CLI (non-TUI surface)

```
sasash                    # TUI
sasash addr <host>        # active address to stdout  →  $(sasash addr ubuntu)
sasash up   <host> [set]  # start master + forward set
sasash down <host>        # ssh -O exit
sasash status             # one line per host, scriptable
```

`sasash addr` is what replaces "searching for the IP for other needs."

### `status` output format

One line per host in config order, always exactly five whitespace-separated fields:

```
<alias> <active-name|-> <active-addr|-> <latency|down> down:<names|->
```

- Active path = first alive in config order (`probe.Select`); `-` as the name marks
  the derived HostName-fallback path.
- The address is the full dial target (`host:port`, IPv6 bracketed) — status reports
  what was probed. `sasash addr` deliberately differs: it prints the bare host, as
  data for `{addr}`-style substitution.
- Latency is integer milliseconds; `down` when no path answered.
- Field 5 lists dead path names (dial address when unnamed), `down:-` when all alive —
  so `NF` is always 5 for awk.

### `up` / `down` semantics

`up <host>` with no set name applies **all** declared forward sets, in meta
order; `up <host> <set>` applies just that one. A host with no declared sets
still gets its master ensured — `up` is never a no-op. An already-running
master is success — `up` starts only what is missing. A mid-sequence failure aborts the
remaining sets but leaves the earlier ones up (the state cache is never
truth; status re-derives reality). A state-cache write failure is a warning,
never a failure: the ssh action succeeded. `down` is `ssh -O exit` and is
**not** idempotent — a master that isn't running reports failure.

### Exit codes (`addr`, `status`, `up`, `down`)

`0` success (`status`: even with hosts down — it reports, it doesn't judge;
`up`: including an already-running master) ·
`1` action failed — `addr`: all paths down, retryable; `up`/`down`: ssh
refused, its message on stderr · `2` usage · `3` config problem
(unknown host, unknown forward set, unreadable config).

---

## 8. Layout

```
cmd/sasash/main.go
internal/run/        the only package that starts a process
internal/sshconf/    parse + round-trip-faithful write of ssh_config
internal/probe/      parallel TCP dial, latency, caching
internal/tunnel/     control-master ops, live state verification
internal/action/     command templates + tea.ExecProcess handoff
internal/ui/         bubbletea models
```

Deps: `bubbletea`, `bubbles`, `lipgloss`. Nothing else required.

---

## 9. Milestones

**M1 — read-only (solves pains 1 & 2).**
Parse configs, list hosts, probe paths with live status, `enter` to ssh, `y` to copy address,
`sasash addr`. Writes nothing. Ship this before building anything else — if M1 isn't useful
daily, the rest won't be either.

**M2 — path switching.** `p` rewrites `HostName`. First write path; get the round-trip parser right here.

**M3 — tunnels.** Named forward sets, master lifecycle, live add/cancel, honest three-state status.

**M4 — actions.** `actions.toml`, `xfreerdp` as the first template.

---

## 10. Verify before building

1. `ssh -O forward -L <port>:localhost:<port> <host>` against a live master on your box.
   If this doesn't work, M3's design changes.
2. `herdr --remote <alias>` with a plain `Host` block in `~/.ssh/config`.
   Confirms herdr honors aliases (it's documented, but confirm).

---

## 11. Migration

Current aliases and what replaces them:

| today | after |
|---|---|
| `ubunter`  = `ssh nadav@192.168.3.141` | `ssh ubuntu` |
| `herdru`   = `herdr --remote nadav@192.168.3.141` | `herdr --remote ubuntu` |
| `herdrut`  = same over Tailscale | same alias — `p` switches the path |
| `ubuntui` / `ubuntuit` = xfreerdp, two paths | one `rdp` action template |
| `spp`, `herdrp` = `203.0.113.150` (user `nadavb`) | `Host spp` |

That's 7 aliases encoding machine × tool × path in their *names*, collapsing to 2 host entries.
The combinatorics are the actual justification for this tool — not "I forget aliases."

---

## 12. Testing

Goal: **every path that can corrupt `~/.ssh/config` or lie about tunnel state is exhaustively
tested.** Not 100% line coverage — `View()` golden strings and `main()` wiring break on cosmetic
changes and catch nothing.

Targets: `internal/sshconf` ~100%, `internal/probe` / `internal/tunnel` / `internal/action` ≥95%,
`internal/ui` smoke only. `go test -race ./...` in CI.
UI behavior tests may use teatest; assertions are conditions and model state, never
full-frame goldens. vhs artifacts are review aids, not gates.

### Seams

Three boundaries, all injectable. Nothing else in the codebase touches the outside world.

```go
// internal/run — the only place os/exec is imported
type Runner interface {
    Run(ctx context.Context, argv []string) (stdout, stderr []byte, code int, err error)
}

// internal/probe — the only place net.Dial is called
type Dialer interface {
    Dial(ctx context.Context, addr string) (time.Duration, error)
}

// injected wherever elapsed time or cache expiry matters
type Clock func() time.Time
```

Fakes live in `internal/run/fakerun` and `internal/probe/fakedial`: table-driven, keyed by
argv or address, returning canned output plus a recorded call log. No live SSH server, no
network, no sleeps in unit tests.

Filesystem is **not** faked — pass a root directory and use `t.TempDir()`. Real files in a temp
dir are simpler and catch more (permissions, line endings, partial writes) than a fake FS.

### What gets tested hardest

1. **`sshconf` round-trip fidelity.** The highest-value test in the project — this code writes to
   the user's real ssh config. Property test: for any input, `Write(Parse(x)) == x` byte-for-byte.
   Add `go test -fuzz` over config text. Corpus of golden files: comments in odd places, `Match`
   blocks, `Include`, tabs vs spaces, CRLF, unknown directives, duplicate `Host`, no trailing
   newline. A parse failure must never produce a partial write — parse fully, then write atomically
   (temp file + `os.Rename`), never truncate-in-place.

2. **Argv construction.** Table test every command in §4 to exact argv. These strings are the
   product; a typo in `-O cancel` silently does nothing.

3. **Tunnel state reconciliation.** Matrix of (state file says X) × (`-O check` says Y) ×
   (port dial says Z) → expected `up` / `listening` / `down`. Reality must win over the state file
   in every cell, including "state file missing", "state file corrupt", and "master alive but
   forward gone".

4. **Path selection.** Fake dialer returns latencies and errors; assert the chosen path honors
   configured order, ignores dead paths, handles all-dead, and never blocks past the timeout.

5. **Port conflict detection** before `-O forward`, including the race where the port is taken
   between check and add.

### Integration tier (build tag `integration`, excluded by default)

A real `sshd` on localhost — `/opt/homebrew/opt/openssh/sbin/sshd -D -p <port>` with a throwaway
host key and an authorized test key in `t.TempDir()`. Proves `-O forward` / `-O cancel` actually
behave as §4 claims against a real OpenSSH. Run locally before each release, not on every save.
