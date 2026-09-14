# Configuring sasash

sasash reads its own settings from `~/.config/sasash/config.toml`
(`$XDG_CONFIG_HOME/sasash/config.toml` when that variable is set). `sasash setup` creates it with every option commented
out beside its default; uncomment a line to set it. The file is yours: sasash
re-reads it before every write and rewrites only the one line a key changes, so a
hand edit is never lost.

Options are top-level `key = value` lines: quoted strings or `true`/`false`.
Comments and blank lines are fine. An unknown key or a value of the wrong type is
reported as a notice when the TUI starts and otherwise ignored; a line the parser
cannot read at all disables the whole file for that run, with the line named. Neither
keeps the TUI from starting.

## Options

| key | type | default | meaning | also set by |
|---|---|---|---|---|
| `theme` | string | `"gruvbox"` | color theme: `gruvbox`, `mocha`, `tokyonight` or `nord` | `t` cycles and `o` → theme picks; both write the choice here. `--theme` and `SASASH_THEME` override for one run without writing |
| `editor` | string | `""` | the editor `e` opens the connections file (`sasash.conf`) in, split on spaces (e.g. `"code -w"`) | empty means `$EDITOR`, then `vi` |
| `show_hidden` | bool | `false` | list dotfiles in the local file browser of `C` (copy files); the remote listing always shows them | `H` inside the local picker flips it and writes it back |
| `spawn_target` | string | `"tab"` | where `enter` opens the ssh session inside herdr or tmux: `pane`, `tab` or `workspace` | `o` → spawn target writes the pick here |
| `spawn_window` | string | `""` | the command that opens an OS window running ssh when no multiplexer is detected; `{host}` is the alias, `actions.toml` placeholders apply. Unset (commented out) means the platform default: Terminal on macOS, `x-terminal-emulator` or `gnome-terminal` on Linux, Windows Terminal under WSL. A live `""` turns that off, so `enter` hands off instead | — |

## Precedence

For every option: command-line flag, then environment variable, then this file, then
the default. A flag naming an unknown theme is an error; an unknown theme in the
environment or in the file falls through to the next source with a warning.

## Migration from the older files

Before this file existed, settings lived in three files: the theme in
`~/.local/state/sasash/theme`, the spawn target in `~/.local/state/sasash/spawn-target`,
and the window command in `~/.config/sasash/spawn-window`. `config.toml` is now the only
place sasash reads settings from. `sasash setup` (or `o` → setup in the TUI) moves each
old file that still exists into `config.toml` — as a live line, unless you already set
that key by hand — and removes it; a file naming nothing sasash can keep (an unknown
theme, say) is removed without a write. Until then the TUI shows a notice at startup
naming the old files.
