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
3. Adding a port-forward without killing the existing tunnel → one more `ssh -N -L` process; the others are untouched (§4).
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
A block anywhere — `~/.ssh/config`, an Included file — may carry them too: any `@sasash` line of
its own makes a host *managed* in the TUI's eyes, and `# @sasash hide` (bare, no arguments) asks
for it to be left out of listings while staying fully usable. Both are hand-written facts; sasash
never guesses either from a name.

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

**The trio is load-bearing for the interactive master (SASASH.4.18).** Every `-O` command
reaches a master only through a ControlPath, and an interactive connect starts one only under
`ControlMaster auto` — so a managed entry without the three directives shows its master down
forever, and `r`/`X` fail with ssh's `No ControlPath specified for "-O" command`. Tunnels
themselves (§4, Model B) are their own processes and need none of it. Every entry sasash saves (`a` then
save, settings → add new connection) carries the trio verbatim; an older managed entry without
it is offered exactly the lines it lacks from that failure's popup (`enter` writes them under
the Host line in one atomic insert, nothing else touched, existing values kept; `esc` closes),
while a host sasash does not manage gets the `e` hint — its block is not sasash's to write. The
`~/.ssh/cm` directory stays setup's job: a save says when it is missing.

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
- **A forward IS a process** (Model B, SASASH.7): one detached `ssh -N -L` per local port,
  grouped into named sets. No shared master on the tunnel path, no `-O`, no ControlPath
  required. Stopping a forward kills its process, and that drops its listener *and* its
  established connections together, touching nothing else.

### Commands (the entire tunnel engine)

| action | command |
|---|---|
| start a forward | `ssh -N -S none -o BatchMode=yes -o ExitOnForwardFailure=yes -L 4014:localhost:4014 <host>` — sasash detaches it itself (own session, stdin/stdout from/to `/dev/null`, stderr to `~/.local/state/sasash/<host>.<port>.log`) and records the pid. Success is the local port answering; the process exiting first is failure, with its stderr as the reason |
| add a port to a running set | the same, once more — the other processes are not touched, nothing restarts |
| remove a forward | SIGTERM to that one process — the listener and the connections through it drop together; the other processes are untouched |
| set down | SIGTERM to each of the set's processes |
| everything off (CLI `sasash down` only) | SIGTERM to every recorded process of the host — no TUI key does this since SASASH.4.25 (`X` deletes one set) |
| interactive master alive? | `ssh -O check <host>` — an indicator for enter/spawn/scp reuse only; never a forward's verdict |
| interactive master down | `ssh -O exit <host>` — tunnels are their own connections and survive it |
| copy files (`C`, SASASH.4.33) | `scp [-r] <source> <dest>` — the remote operand is `<alias>:<path>`, the path verbatim (scp hands it to the host's sftp server as typed); `-r` exactly when the source is a directory. The alias alone: scp reads the same config, so the copy rides the block's active `HostName`, its `ProxyJump`, and a live master (auth once). Runs in the foreground through `tea.ExecProcess` like `S` — native progress meter and prompts on the terminal — with stderr captured for the outcome, since the alt screen hides scp's own line once the TUI resumes; a non-zero exit is data: the notice carries scp's last stderr line and the popup the argv with every line |
| remote browser listing (`C`) | `ssh -o BatchMode=yes <alias> ls -1Ap -- <path>` — through the run seam, bounded by the status poll's timeout; `BatchMode` for the reason the tunnel rows carry it: no tty exists behind the TUI to prompt on, so a host that needs a passphrase, a password or a host-key answer fails at once (ssh exits 255) with a popup carrying the argv and ssh's words and a notice pointing at `S` — one interactive connect, and the master carries the listing. `-p`'s trailing slash is the one fact parsed (never `ls -l`: its columns are locale- and platform-shaped; a symlink to a directory carries no slash and reads as a file), `-A` skips `.`/`..`, `--` guards a leading dash; the path is single-quoted for the remote shell only when not plain. Starts at `.` (the login home, as scp sees it) and walks by `path.Join` (`..` above home keeps counting — no absolute home is guessed). A non-zero exit — no such directory, permission denied — is the host's refusal shown as a notice with ls's words; the browser stays where it was, and a listing that lands after the browser moved on (another host, another directory) is dropped |

Why the flags: `BatchMode=yes` because no tty exists to prompt on — a key that needs a
passphrase or a host that needs a password fails at once with ssh's reason instead of
hanging (keys + agent make the per-process auth invisible; a password-only host means
connecting once interactively, which the hybrid's `ControlMaster auto` then reuses).
`ExitOnForwardFailure=yes` because a `-L` that cannot bind would otherwise leave a live
process serving nothing; with it the process dies and its stderr says
`Address already in use` — the honest answer to a held port. No `-f` because `ssh -f`
forks after auth and the pid we would hold is the one that exits; sasash is the
backgrounder, so the pid it records is the tunnel's own. `-S none` (ControlPath none)
because the managed entry's trio would otherwise turn the process into a shared master:
with no master alive, `ControlMaster auto` + `ControlPersist` fork it to the background
and the recorded pid exits 0 (the `-f` problem by another road — measured on OpenSSH
10.2, SASASH.7.1); with a master alive, ssh hands the `-L` to the master and exits, so
the listener would be the master's and a kill would drop nothing. A tunnel process never
shares a connection; its listener lives and dies with the pid sasash records.

Hybrid: the ControlMaster trio stays in every managed entry for interactive reuse (auth
once for shells and file copies). The one thing the master alone could do — graft a `-L`
onto a live interactive shell — is not carried over.

Every tunnel sasash starts goes through this engine — the ad-hoc `-N` included (SASASH.4.17):
it is a session-only (unsaved) forward, recorded like any other, so Status shows it and
`x`/`X` end it. The one raw run left is the one the user chooses with that said up front
(the `a` row).

### The thing that will bite you
**A recorded pid is a promise, not a fact.** The process can die outside the TUI, the
machine can reboot and hand the number to something else, and the state file will not
notice.

Therefore, on **every** render, per forward:
1. is the recorded pid alive? (`wait4`/`kill 0` — a syscall, never a guess)
2. `net.DialTimeout` to `127.0.0.1:<localport>` → listener present?

Reality wins over the state file, always. Report three distinct states, don't collapse them:

- `● up` — the recorded process is alive *and* the local port accepts
- `◐ listening` — the port accepts but no live sasash process owns it (someone else runs it,
  or the record was lost)
- `○ down` — anything else, including a process that runs but serves nothing

A tunnel manager that lies about what's up is worse than no tunnel manager.

### Port conflicts
Before adding a forward, dial `127.0.0.1:<port>`. If something already holds it, say so and
offer the next free port. Never silently remap — the engine's spawn dials first too and
refuses a port that already answers (a foreign listener would make its own readiness dial
lie), and `ExitOnForwardFailure` catches the one that appears in between.

---

## 5. TUI

Two panes. Left: filterable host list. Right: detail for the selected host.

Presence (SASASH.4.29). Every host row is glanceable: `› ● api        67ms  2 up` — the
reachability glyph (`●` up / `○` down / `◌` pending) and latency of the path ssh dials (the
block's `HostName` path, the same rule as the detail's `← active`; first alive in config order
when no path is the HostName), and the host's forwards up as of its last poll (blank when
never polled or none up). Rows are tabular over the longest listed alias, a long alias is cut
with `…` so the columns survive, and the count column is reserved whenever the tunnels
section is on and the pane keeps 12 alias cells beside it (a narrower pane drops the column
whole rather than clipping every alias), so the detail pane never shifts. The rows read the maps the detail pane
already reads — never a probe, poll, or config read of their own. The header is a wordmark
band (SASASH.4.32): a filled figlet face (ANSI Shadow) centered across the frame on its own
rows, then the `»»»»` wake leading the tally on a centered line under it — palette-colored,
the same solid glyphs under `NO_COLOR` — when the terminal has at least 32 rows and the
columns for the face, else the one-line title with the tally beside it; the tally has its
own line, so the wordmark never evicts it. On a terminal wider than 120 columns the frame is
centered horizontally. The cap places, it never cuts: every line is clamped at the terminal's width
as before, and a line longer than the frame (a command preview, a plan row) shifts the whole
frame left to fit whole rather than losing its tail; vertically the frame stays top-anchored.
The status columns yield to alias room on a narrow pane — the count column first, then the
latency; the glyph and a whole alias stay.

The list is attention-ordered, not capability-ordered: *managed* hosts — declared in
`sasash.conf`, or carrying any `@sasash` meta of their own — list first, in config order;
every other ssh_config entry follows below a blank spacer and a divider row (`other
ssh_config entries`, or a shorter wording when the pane cannot fit it whole), dimmed via the
palette — only while both tiers are listed; a lone tier is neither divided nor dimmed. Both
tiers do everything. The split is decided from facts the host
model carries (the declaring file, meta presence), never from a name. The cursor never
rests on the divider; `/` searches both tiers and drops the divider; the header tally says
`N hosts (M managed)` while both tiers are listed. A host with `# @sasash hide` is not
listed at all — still usable everywhere, and a filter term naming its whole alias reveals
it, as with a folded variant.

```
 sasash                                    /  filter   ?  help

 › ubuntu          paths  ● lan  192.168.3.141   20ms   ← active
   spp                    ○ ts   100.105.178.123 down
   bastion
                   tunnels
                    ● web   3000, 3001              up 12m
                    ○ api   4011, 4012, 4013        down
                    ● ad-hoc 4014                   up 2m

                   $ ssh -N -S none -o BatchMode=yes -o ExitOnForwardFailure=yes -L 4014:localhost:4014 ubuntu
```

The command line at the bottom always shows what the highlighted action will run, except
while a transient notice (an action's outcome, ssh's own message) holds the line — the
next keypress clears it. On the host row it also names `H`'s command beside `enter`'s
when both fit; `c` copies the `$` command (the highlighted action's), never the `H` one.

| key | action |
|---|---|
| `enter` | spawn `ssh <host>` beside the TUI through the surrounding multiplexer (see *Spawn* below); with nothing to spawn into it is the `S` handoff plus a notice |
| `S` | ssh to host in this terminal — the classic handoff: the TUI suspends until the session ends |
| `H` | `herdr --remote <host>` beside the TUI (SASASH.4.22): the spawn road, the TUI staying up, never a handoff. `H` checks `HERDR_ENV` FIRST, before any multiplexer routing. Set means herdr is above us — the pane itself, or a tmux started from one (a tmux server snapshots its environment for life) — and anything herdr or that tmux opens inherits the variable herdr's nesting guard reads, so `herdr --remote` there would nest and be refused. `H` then goes up and out: a new tab of the OUTER terminal emulator, a fresh top-level process that is no child of herdr, where `herdr --remote` starts clean. The emulator is read off the environment: wezterm (`WEZTERM_PANE`, or `TERM_PROGRAM=WezTerm`) → `wezterm cli --no-auto-start spawn -- herdr --remote <host>`, run under `env -u` of herdr's five pane variables — wezterm's CLI forwards its own environment into the pane it spawns, so left in place they would make the guard fire on a process that is not nested — and of `WEZTERM_UNIX_SOCKET` / `WEZTERM_PANE` / `TMUX` / `TMUX_PANE` — the first two stale under a GUI restarted since herdr began (without them the CLI finds the running GUI through its default socket link), the tmux pair a forwarding artefact when the TUI runs in a tmux inside herdr; kitty (`KITTY_WINDOW_ID`) → `kitten @ launch --type=tab --tab-title <host> herdr --remote <host>` (needs `allow_remote_control`); iTerm2 (`TERM_PROGRAM=iTerm.app`) → `osascript` creating a tab and writing the command; Terminal.app (`Apple_Terminal`) → `osascript … do script` — a window, its scripting has no tab. For the two AppleScript apps the command is one shell line, quoted per word and passed as a script argument, never spliced into the script. Under tmux with no herdr above, the tmux window/split/session per the spawn target runs `herdr --remote <host>`. Outside any multiplexer, the emulator tab again — the OS-window template bakes its own `ssh {host}` in and cannot carry `H`'s command. No emulator sasash can drive → a popup naming the command to run yourself in a tab outside herdr; never plain `ssh`, never a herdr tab, never a handoff. The footer names the command and where (`H herdr --remote api (wezterm tab)`); the failure popup names the exact argv. Verified live on wezterm; the kitty, iTerm2 and Terminal.app argv follow their documented CLIs and are unverified on target. The two AppleScript apps type the command into the tab's login shell, so the user's rc files run first — an rc that auto-runs herdr would nest there; wezterm and kitty exec the program directly |
| `y` | yank active host to clipboard (bare address, `sasash addr` parity; the pane shows `host:port`). The clipboard is OSC 52 first — the terminal's own escape, written to `/dev/tty` through the run seam, which works in modern terminals on macOS, Linux and WSL and over ssh — then a detected tool behind it (`pbcopy`, `wl-copy`, `xclip -selection clipboard`, `clip.exe`: the first on PATH that takes the text — a `wl-copy` failing on X11 yields to `xclip`; each runs with its stdio detached, so a selection daemon cannot hold the seam open), both as exact argv with the text as a positional argument; the notice names what took it (`copied via terminal (OSC 52) + wl-copy`), and when the terminal escape was the only leg it says that it cannot be confirmed (OSC 52 has no reply; GNOME Terminal and macOS Terminal drop it, and inside tmux it reaches the outer terminal only when tmux's `set-clipboard` is on and the terminal's terminfo advertises `Ms` — often not, so it is swallowed; the hedge fires only when the escape was the sole leg, a tool behind it settles the matter). Floor: when neither can — no terminal to write to and no tool, or every tool failed — never a silent no-op: the notice says `clipboard not available — <value>` and the popup shows the value whole with what was tried, so it can still be copied off the screen. `c` copies the same way |
| `p` | pick the selected host's active path in the paths pane itself: the focus moves onto the path rows already rendered — glyph and latency in place; `← active` names the block's `HostName` row, the thing enter changes — the path `ssh <alias>` actually dials, in and out of the focus (never first-reachable; a `HostName` matching no path marks none active) — SASASH.7.2 — with the `›` mark on that row (last match, case-insensitive; the first row when it matches none, and the plan says `HostName … matches no named path`); a `›` column appears on the path rows only while the focus is held, so the rows keep their width otherwise; `j`/`k` (`g`/`G`, `ctrl+d`/`ctrl+u`) move the mark over the named rows, `enter` rewrites `HostName` to the marked path (`pathswitch.Set`; re-picking the active one still regenerates the pinned variants) and returns to the list, `esc`/`h` return without writing; the bottom line names the plan (`p → ts 198.51.100.5` — the port follows the switcher's ownership rule at write time, so it is not guessed; `(already active — variants regenerate)` on the active row). The focus follows the 4.5 rules: the list dims, action keys keep their meanings, any host change drops it. `p` reads the block from disk when it takes focus: the block decides whether a switch is possible, and the rows offered are the rendered named paths that block also declares (`pathswitch.Set` works by name; the plan and the active row are judged by the block's address for that name, which is what enter writes, even when the pane shows another) — a pane rendering another block's paths (a `Host` above the sasash `Include` shadows it, or an edit not yet reloaded) is said so: `p: the paths on screen are not sasash.conf's — e reloads on return`, and the host row's line says `p: pane rows are not sasash.conf's`. Without a choice there is no focus, only a notice: `p: not managed by sasash`, `no named paths — nothing to switch`, or `only one named path — nothing to switch`; the host row's line previews the choice (`p: pick lan / ts`) or the lack of one (`p: only one path`, `p: no named paths`, `p: not managed by sasash`). The outcome is a notice — `HostName: lan … → ts …`, plus `N pinned variants left as-is (names)` when the generator does not own a variant block — then the config is re-read and re-probed and the switch is remembered for `last switch` |
| `l` / `tab` | the pane-focus cycle (SASASH.2.11): from the host list, focus the paths pane when the host has a path to choose (a managed block with two or more named paths — exactly what `p` gives, the same flow and the same one read of the block; a block with a choice the pane does not render keeps `p`'s diagnostic on the way past) and the tunnels pane otherwise (a host without tunnels says so instead); under the paths focus `l` moves on to the tunnels pane (a host without tunnels says so and returns to the list — the cycle closes either way), under the tunnels focus `l` returns to the list, so the cycle is list → paths → tunnels → list. Inside a pane `tab`/`shift+tab` move the highlight like `j`/`k` (the pane's own convention), `ctrl+d`/`ctrl+u`, `g`/`G` as well, and the bottom line previews the row's action; `h` / `esc` return to the host list from either pane, as does any host change. The focused pane's selection is lit and carries the `›` mark, the other's is dim and unmarked |
| `space` | toggle the highlighted FORWARD (SASASH.4.31): up (a process of ours, port answering) → SIGTERM that one process; anything else → start its one process (`Spawn`, §4's start row), the set's other forwards untouched. Per-forward start AND stop, symmetric; the preview line shows the one command it will run. Known hole: a recorded process that is alive but whose port stopped answering shows `down` (reality wins), so space starts a second process and the record moves to it; `x` BEFORE space ends the survivor (it SIGTERMs the recorded pid), but once the replacement is recorded the survivor is no longer reachable from sasash and has to be killed by hand |
| `s` | toggle the highlighted row's whole SET (what `space` did before 4.31): `SpawnSet` — one process per forward not already up — or, when every forward is up, `KillSet` |
| `+` | add a forward set, PERSISTED BY DEFAULT (SASASH.4.21): the spec prompt (port shorthand expands; a leading `!` makes it session-only), then a set-name prompt defaulting to the FIRST forward's local port — enter writes one `# @sasash forwards <name> <specs>` line (`fwdset.Add`) AND connects it live (`SpawnSet`), so the forwards are up now and survive sessions/reboots; `s` then toggles the set non-destructively (it is a declared set). The prompt accepts MULTIPLE whitespace-separated specs (SASASH.4.27) — `4011 4012 4013` is one set with three forwards — matching the space-separated config format; each token expands via the §5 shorthand independently, the preview shows every expanded spec, and the set is all-or-nothing: a malformed or duplicate token names itself and adds none. A held local port comes back as the remap offer (single spec) or, for a multi-spec set, names every held port with its next-free and refuses the whole set — never a partial add. EPHEMERAL opt-in: a leading `!` on the spec is the old inject — one `ssh -N -L` process, recorded so `x`/`X` can end it, but NOT written to `sasash.conf`; the preview and notice label it `session-only (unsaved)`. On an UNMANAGED target (no sasash.conf block to declare into) + is session-only with a notice to save the host first (o/settings) to persist — never a two-prompt failure |
| `x` | disconnect ONE forward: SIGTERM its process; the notice says its connections dropped with it (§4's remove row). The declaration stays — `space` reconnects it. Kept beside `space` as the explicit stop: it never starts anything, and on a row with no process of ours it says so instead of spawning |
| `E` | edit the highlighted forward in place: the prompt opens prefilled with its spec; enter rewrites that one spec on the set's meta line and, if the forward is live (row up: a process of ours), kills the old process and spawns the new spec — the row shows both commands while you type; every other forward is untouched; row not up = config only |
| `n` | declare a new forward set on the selected host: the set-name then specs prompts (the settings add-tunnel flow, surfaced in the pane; port shorthands expand), written as one `# @sasash forwards <name> <specs>` line through `fwdset.Add`. Desire ONLY — nothing is started; the notice says `s` connects it (SASASH.4.19; `s` since 4.31) |
| `r` | exit the interactive master (tunnels stay; `ControlMaster auto` starts a fresh one on the next connect) |
| `X` | delete the highlighted set's DECLARATION — the tunnel lifecycle's fourth verb (SASASH.4.25 folded the old kill-all into this; press `X` again for the next set). Distinct from `x` = disconnect ONE, keep the declaration. No confirm popup (SASASH.4.30 removed it as friction): `X` acts at once — `fwdset.Remove` drops the `# @sasash forwards …` line; when the set is LIVE it is a two-step — `KillSet` downs its per-port processes, then the line is removed. Never silent: the notice says `deleted <name> on <host>`, with ` — N tunnels stopped` when processes were downed. `X` on an AD-HOC row (state-cache only, no declaration) forgets the record instead: killed first when live (`forgot <spec> on <host> — 1 tunnel stopped`), `sasash.conf` untouched. There is no user key that nukes every tunnel at once |
| `e` | open the file that DECLARES the selected host in `$EDITOR` — `sasash.conf`, `~/.ssh/config`, or the Included file its block lives in — and reload on exit; the setup hint (`sasash.conf` not wired into ssh) shows only when that file is `sasash.conf`. Settings' "edit config file" always opens `sasash.conf` |
| `R` | rename the selected host (SASASH.4.16): the prompt opens prefilled with the alias; enter renames, esc cancels. Managed blocks only — sasash writes only `sasash.conf` — so an ambient row gets a notice pointing at `e`, and a pinned variant points at its parent. The name follows the 2.3 save rules (a single plain word, no wildcards, no leading `-`/`#`, no collision with ANY listed host, ambient tier included, nor with a `<new>.<path>` the fan-out would create). One atomic write: the `Host` line, and every generator-owned `<old>.<path>` variant becomes `<new>.<path>` (hand-authored dotted blocks never follow — the notice counts them as left as-is); `@sasash` meta stays with its block. The state caches (`<host>.json`, `<host>.stats.json`) follow the host, so observed history survives — a cache is never overwritten: a leftover under the new name keeps both files, and the popup names them; the list reloads with the selection on the renamed row. A running master is NOT touched, and it follows: sasash's `ControlPath ~/.ssh/cm/%C` hashes HostName, port and user — none of which a rename changes — so the same socket answers `-O check <new>` at the next poll (an alias-keyed ControlPath would need a reconnect); when a master is up the line says `the master follows` and the popup says why |
| `c` | copy the displayed `$` command instead of running it |
| `.` | toggle the ambient "other ssh_config entries" tier (SASASH.4.26). Hidden by default WHEN managed hosts exist (a list that would otherwise be empty still shows them); the divider and the ambient rows appear only while it is on, and the header tally counts them then. A `/` filter term still finds an ambient host by an explicit name without the toggle |
| `$` | toggle the command-preview footer line (SASASH.4.26). Hidden by default: the permanent `$ …` line is noise, and for a spawn it shows a non-runnable `<pane>` placeholder. Off, the footer stays quiet; honesty is preserved — `c` still copies the command, and prompts, popups and notices still show the real commands — so nothing is hidden that cannot be seen another way |
| `t` | cycle theme (choice persists) |
| `o` | settings, a centered popup (the 4.11 box, SASASH.4.14) in a fixed frame (4.15: 60 content cells wide — room for the menu's longest row, a test keeps it so; a longer value such as a config path outside home is cut, never wrapped — and the menu's six rows tall, clamped to the terminal like any popup — a search, a picker or a second visit never moves it; rows scroll or pad inside, the no-match line included; the setup confirmation alone is as wide as the pieces it lists, so entering it from the menu is the one visible change of width): create config file (the setup flow, confirming exactly what is written), edit config file (`e`), theme picker, spawn target picker (`pane` / `tab` / `workspace`, persisted), add new connection (save first, then offer to connect), add new tunnel (a durable `# @sasash forwards` line — desire only, nothing is started). vim keys navigate; action keys are inert while it is open; `esc` backs out one level. On a frame too short for a pane's rows the box scrolls like any popup — the bottom line says `lines a–b of n · j/k scroll` ahead of the pane's footer, and `j`/`k` reach every row, the setup pane's pieces included (unmarked there: they are read, not chosen). `/` searches the menu vim-style: each typed character narrows the entries to those containing the text (case-insensitive substring), the highlight lands on the first match and `↑`/`↓` move it among the matches (`j`/`k` are text while typing), `enter` activates the highlighted entry, `esc` drops the search and shows the full menu with the highlight where it was; no match says so (`no match for /zz — esc clears`). The sub-panes (setup confirm, pickers) open in the same box; the prompts (add connection, add tunnel) close it and use the bottom line as before |
| `C` | copy files with scp (SASASH.4.33): local ↔ the selected host, both ends browsed before anything runs. Three steps in the body: the direction (`upload` local → host, `download` host → local; `j`/`k`/`enter`, or `u`/`d` straight in), then the SOURCE in one browser, then the DEST directory in the other. The local end is the bubbles filepicker over `$HOME` (`j`/`k`/`g`/`G`/`ctrl+d`/`ctrl+u` move, `l` opens a directory, `h` goes up, `enter` selects the highlighted file or directory, `.` selects the directory the picker is in; hidden entries not listed; the `d` of the permissions column is the directory glyph); the remote end is a browser over `ssh -o BatchMode=yes <alias> ls -1Ap -- <path>` (§4) with the same keys, a synthesized `../` row on top, directories first with their trailing slash. A directory source makes the copy `-r`; a file picked as the dest is refused with the way to pick a directory. `esc` backs out one step (dest picker → source picker → direction → closed); `?` and the action keys are inert inside, as in a prompt. The bottom line shows the scp as it stands — `scp -r /home/u/proj api:<remote dir>` — the end not yet picked as a placeholder, whatever the `$` toggle says. The dest pick opens a confirm popup naming the exact argv; `enter` hands the terminal to scp (§4's row: the S handoff's road, progress and prompts native), `esc` keeps the picker. On return the outcome is a notice — `copied <src> → <dst>`, or `scp: <its last stderr line>` with the popup carrying the argv and every line — and the paths are re-probed. Ad-hoc only: no transfer history, no saved transfers, no rsync, no remote → remote (a later slice, if any). Nothing here resolves a HostName: scp and the listing name the alias, so a switched path (`p`) or a live master is theirs for free |
| `a` | ad-hoc connect: `[user@]host[:port] [-L spec]… [-p port]` in any order — a pasted `ssh …` command works, the leading `ssh` is dropped; offer to save on return. An input with flags or specs but no host token targets the SELECTED host — its alias, so its config and ControlMaster apply, ambient-tier hosts included — and says so at the head of the expansion line before enter (`→ target: ubuntu (selected) · ssh -L … ubuntu` — leading, so a narrow frame's ellipsis never eats it), never silently; the alias goes in verbatim (a hand-written `web:8080` is the alias, not a port); a typed `-p` still goes to ssh and beats the block's `Port`, as the line shows (not under `-N`: a saved alias takes no user or port there — the line says so, and enter refuses); an explicit target is untouched, an empty list keeps the no-target error, and no save is offered for a selection-defaulted target (it is already saved). This deliberately overlaps `+`: `-N -L 1234:localhost:8080` on the selected host is `+` with the spec, by another road — both stay. With `-N` it is tunnel-only, and it goes through the tunnel engine (SASASH.4.17) so the tunnel is recorded, shown in the tunnels pane as a session-only (unsaved) forward and stoppable with `x`/`X`: a target that is a saved host — the selection, or a bare alias typed — runs `+`'s flow under the host's in-flight gate: one spec is `+` exactly (the local port dialled first, a held port answered with the remap offer), several go straight to the engine (one `ssh -N … -L <spec> <alias>` process per spec, §4; a held port is refused before anything starts), and the expansion line shows exactly the steps that would run; a `-N` with no `-L` has nothing to start and says so; a saved alias with a user or port beside it is refused in the prompt with the reason — it is neither the alias nor a destination a save could record honestly. The engine's processes carry BatchMode too (§4), so a saved host needs keys/agent or one interactive connect first. An unsaved target is saved FIRST — the alias prompt opens before anything starts, saying why, and the saved alias carries the same flow. Declining the save (esc, or an empty alias) is honest up front: a confirm popup names the raw `ssh -fN -o BatchMode=yes …` that would run outside sasash — no ControlMaster, not in the pane, not `x`/`X`'s to stop — and how to end it (`pgrep -fl 'ssh -fN'`, then kill the pid); `enter` runs it anyway and its outcome popup repeats the cleanup, `esc` runs nothing. BatchMode makes that raw run refuse to prompt (`-f` would otherwise open `/dev/tty` over the TUI), so auth must be keys/agent or the failure popup says so with the hint to connect once without `-N`. Nothing ever starts silently invisible |
| `/` `?` `q` | filter, the keys panel, quit. `?` opens a panel over the body (the 4.11 popup, so it scrolls with `j`/`k` and owns the keyboard) listing the action bindings grouped by surface — host list, paths (`p`), tunnels (`l`), prompts, settings (`o`), popups, plus the `actions.toml` names — the keys and their wording taken from the bindings themselves (a binding's group is the one hand-kept list, and a test walks every keymap to catch an action missing from it), with the group of the surface holding the keys first; the vim motion keys are omitted (SASASH.4.35) — `j`/`k` and the arrows, `g`/`G`, `ctrl+d`/`ctrl+u`, the `h`/`l` pane and directory hops, and the transfer's `u`/`d` direction toggle — because movement is self-evident and each pane names its key in a heading, so they are advertised only in the contextual footers; `?`, `q`, `esc` or `enter` closes it. `/` searches it the same way (4.14): the rows narrow to those whose key or description contains the text, under their group headings, `enter` keeps the narrowed rows (so `j`/`k` scroll them) and `esc` clears back to every row before a second `esc` closes; while typing, `?` and `q` are text. There is no `n`/`N`: with the rows narrowed to the matches there is no next match to jump to — the narrowing is the match list — so the keys would add a mode and nothing else. The prompts row unions every prompt's enter/esc verbs and points at the prompt's own footer, which says which applies; the filter's two keys show only in its footer. It replaces the inline expanding help entirely |

Generated pinned variants fold into their parent by default: a host is a variant only when
its alias is `<parent>.<name>` for a listed parent and one of that parent's path names, it
has exactly one path, that path's host is the parent's path host, and it carries no
`@sasash` meta of its own — everything a generated block is and a hand-written one usually
is not — so a user host that merely looks dotted stays listed. A folded variant is still a
real host (ssh, herdr, everything) once surfaced: the generated variants are an ssh-alias
implementation detail for external tools, not TUI rows, so they always fold — paths live in
one place, the paths pane (SASASH.4.23 retired the `v` toggle and the parent's pinned-variants
line). The header tally counts listed hosts only, and a filter term naming a variant's full
alias or its `.name` suffix (`.office`, `web.office`) surfaces that variant while the term
stands — a bare `web` does not.

The footer is minimal (SASASH.4.13): the command-preview line above it stays exactly as it is — it is the honesty surface, not help — and the key-hint strip on the main view is a whisper, `? keys · q quit`, nothing more. A keyboard owner (a prompt, the filter, settings, a popup) or a pane focus keeps its one-line context hint (`enter add · esc cancel`, `enter make active · esc/h keep current`), because that line says what enter does there. Nothing is advertised nowhere: `e` and `t` are listed in the panel (host list group) as well as in settings.

The header carries a tally — `N hosts · M reachable · K tunnels up (P polled)` — that is
honest about partial knowledge: `probing…` before the first round, tunnels counted only over
hosts polled so far, and dropped when it would not fit beside an applied filter term. Rows
say `up 12m` / `down 3m` from the stats store's observed transitions — since first *observed*
alive or dead, not since the master started — and nothing when unknown; `last connected` and
`last switch → path` appear once recorded. Paths carry session memory only: a sparkline of the
newest probe latencies that fit (`·` for a miss), their min–max, and `answered/rounds` this
session — forgotten when the TUI exits.

Forward specs accept two shorthands wherever one is typed — `+`, `E`, both settings prompts
that take specs, and `-L` in `a`: `3000` is `3000:localhost:3000` and `2000:3000` (two
all-digit fields) is `2000:localhost:3000`. Expansion happens as the input is parsed, so the
writer, the engine, the meta lines, and the status matching only ever see canonical
three-part specs; while you type, a line under the prompt shows the expansion (`→
3000:localhost:3000`, or the whole expanded `ssh …` command in `a`), cut with a visible `…`
if it cannot fit. Anything else passes through as typed and meets the surface's usual rule
— a two-field input like `3000:web` is the malformed spec it always was in `a`; a hostname
is never guessed into a shorthand; an ssh-valid form the engine can judge by its local port
(`3000:/var/run/docker.sock`) is untouched. Shorthands are an input convenience only: a
hand-written `# @sasash forwards web 3000` in `sasash.conf` is not expanded.

The tunnels section renders the engine's three states verbatim, with one presentation
cap: when two forwards claim the same local port, only the first claimant (declared sets
first, in meta order, then remembered ad-hoc ones) can be the one answering, so every
later claimant shows as `◐ listening` with an "unverified" note even when the engine
reports it up — a stale remembered spec cannot ride a live one's listener.

**Feedback tiers (SASASH.4.11):** routine one-liners stay on the bottom line and the next
keypress clears them — a theme saved, a command copied, a filter cleared, a single-path
no-op. Attention-worthy content goes to a popup instead: a dismissable, palette-styled
box centered over the body (SASASH.4.14: bordered, so it reads as a popup with or without
color; sized by its whole content — as wide as its widest row, as tall as its rows up to the
room — so a `/` search narrows the rows inside a frame that does not move (4.15); clamped
to the terminal; on a frame too short for it the blank under its title goes first, then
the border) with multi-line content (long lines wrap, nothing is
cut; `j`/`k` scroll when it outgrows the frame; `/` narrows its rows like the keys panel's).
The box is the theme's signature (4.15): a reference — settings, the keys panel — has the
accent border and bold accent title, an attention popup the warning color's, so the two kinds
read apart by color as they do by their `?` and `!`; inside, group headings take the accent,
keys and live values (the theme, the spawn target) the secondary accent, descriptions the
plain foreground, the highlighted menu row the list's accent-over-fill selection, and a
picker's `○` is dim — a choice not in effect, not a failure. Every role is a palette token
(the four themes each give it a real value); nothing carries color alone.
It owns the keyboard like the settings panel —
`esc` or `enter` closes it, action keys are inert — and what goes there is: an ssh or
action failure with its whole reason (the argv it ran and every line of stderr, where the
bottom line shows one), a reload with more than one warning (the list, where the line
shows a count), a spawn that failed with the argv it tried, an ad-hoc `-N` that failed with
its reason and the auth hint, and an unreadable tunnel or stats cache (what it means, the
file's name and directory, once per host per session; the tunnels pane keeps its marker on
every poll). One popup at a time: a newer one replaces an open one and names what it
replaced. Probes and status polls keep landing behind it, and the bottom-line notice still
carries the one-line form, so closing the popup leaves the outcome on the line.

**Spawn (ratified 2026-09-03):** `enter` opens the connection *beside* the TUI so the
switchboard never leaves the screen: sasash runs the surrounding multiplexer's own CLI — a
previewed command the user could type — through the run seam. Detection order, innermost
first, first match wins: `TMUX` set → tmux; `HERDR_ENV` set → herdr; neither → the
OS-window template; no template → the `S` handoff with a notice. The spawn target is a
settings entry, persisted beside the theme (`~/.local/state/sasash/spawn-target`; default
`tab`), and maps per multiplexer:

| target | herdr (two steps: create, then `herdr pane run <pane> ssh <host>`) | tmux |
|---|---|---|
| `pane` | `herdr pane split --pane $HERDR_PANE_ID --direction right --no-focus` (`--current` without the id) | `tmux split-window -h -d ssh <host>` |
| `tab` | `herdr tab create --label <host> --no-focus` | `tmux new-window -d -n <name> ssh <host>` |
| `workspace` | `herdr workspace create --label <host> --no-focus` | `tmux new-session -d -n <name> ssh <host>` |

`<name>` is the alias with `.` and `:` turned into `_` — tmux refuses them in window names —
and only the name: the ssh argv keeps the real alias. A tmux session is named by its window,
not `-s`, so a second shell to the same host does not collide with the first.

The created pane's id comes from the herdr reply (`result.root_pane.pane_id` for a tab or
workspace, `result.pane.pane_id` for a split); the bottom line shows both steps with the id
as `<pane>` until it is known. `--no-focus` / `-d` keep the TUI where it is — switch when
you want to. Outside any multiplexer the template in `~/.config/sasash/spawn-window` (one
line, actions.toml placeholders, `{host}` is the alias; `$XDG_CONFIG_HOME` honoured; the default
per platform: under WSL (a Linux build with `WSL_DISTRO_NAME` set and `cmd.exe` on PATH — the
interop enabled; without it the Linux probe below applies) `cmd.exe /c wt.exe new-tab wsl.exe -d '<distro>'
-- ssh {host}` (through `cmd.exe` because `wt.exe` is an app execution alias the WSL interop
cannot always exec directly — unverified on a real WSL box; the bare form may also work), on
Linux `x-terminal-emulator -e ssh {host}` when the Debian alternative is on PATH, else
`gnome-terminal -- ssh {host}` when that is, else none (other desktops set the file), on macOS
`osascript -e 'tell application "Terminal" to do script "ssh {host}"'`; a present but empty file means no template, which is how a default
is turned off) runs instead.
The outcome is a notice (`spawned ssh web → herdr tab`, or the failing command and its
reason — herdr's error message, else the tool's stderr or stdout; a failed window command's
popup also names the `spawn-window` file, so a broken default is the user's to fix); each step is given ten
seconds before it is reported as a failure. A spawn counts as `last connected`. `c` on the
host row copies `ssh <host>`, the connection the line makes — the spawn line itself is two
steps and a placeholder, not a shell command. tmux comes before herdr because a tmux
started inside a herdr pane sets both — and carries that pane's `HERDR_*` into every pane it
ever creates, even once the pane is gone — while the user is in the tmux; the reverse
nesting does not exist. Every spawned session rides the alias's ControlMaster, so `X`
(`ssh -O exit`) tears down the master and every session on it, tabs included — sasash
cannot enumerate those sessions (OpenSSH exposes master liveness only), and closing a tab closes
just that session. No embedded terminal, no daemon: the master socket is the state.

**Handoff:** `S` uses `tea.ExecProcess` to suspend the TUI and give the terminal
fully to `ssh`. Native resize, mouse, colors, `~.`, nested tmux — all free. On exit you're
back in the list.

### Keybindings & themes (ratified addendum)

Vim-style navigation is required alongside the action keys above: `j`/`k` (and arrows)
move, `g`/`G` jump to first/last host, `ctrl+d`/`ctrl+u` half-page, `esc` goes back /
clears the filter. Every binding is a named bubbles `key.Binding` so the `?` panel is
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

The CLI takes no typed forward specs — `up` applies declared sets, which are canonical
three-part specs on their meta lines (the §5 shorthands are a TUI input convenience).

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

**Platforms (SASASH.6):** macOS and Linux natively, Windows through WSL only — the Windows
OpenSSH port lacks ControlMaster multiplexing, the engine everything here rides on. `make check`
cross-builds and vets for `GOOS=linux`, so a darwin-only regression cannot land.

## 8. Layout

```
cmd/sasash/main.go
internal/run/        the only package that starts a process
internal/sshconf/    parse + round-trip-faithful write of ssh_config
internal/probe/      parallel TCP dial, latency, caching
internal/tunnel/     control-master ops, live state verification
internal/action/     command templates: actions.toml + the spawn-window template
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
   product; a typo in `-o ExitOnForwardFailure=yes` silently changes what a held port does.

3. **Tunnel state reconciliation.** Matrix of (state file says X) × (recorded pid alive? Y) ×
   (port dial says Z) → expected `up` / `listening` / `down`. Reality must win over the state file
   in every cell, including "state file missing", "state file corrupt", and "master alive but
   forward gone".

4. **Path selection.** Fake dialer returns latencies and errors; assert the chosen path honors
   configured order, ignores dead paths, handles all-dead, and never blocks past the timeout.

5. **Port conflict detection** before a spawn, including the race where the port is taken
   between check and add.

### Integration tier (build tag `integration`, excluded by default)

A real `sshd` on localhost — the first of `/opt/homebrew/opt/openssh/sbin/sshd`, `/usr/local/opt/openssh/sbin/sshd`, `/usr/sbin/sshd`, `/usr/local/sbin/sshd`, `/usr/bin/sshd` that exists, run as `sshd -D -p <port>` with a throwaway
host key and an authorized test key in `t.TempDir()`. Proves spawn / kill actually
behave as §4 claims against a real OpenSSH. Run locally before each release, not on every save.
