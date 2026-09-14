# Configuring sasash

sasash reads its own settings from `~/.config/sasash/config.toml`
(`$XDG_CONFIG_HOME/sasash/config.toml` when that variable is set) — the same
directory as `actions.toml`. `sasash setup` creates it with every option commented
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
| `editor` | string | `""` | the editor `e` opens `sasash.conf` in, split on spaces (e.g. `"code -w"`) | empty means `$EDITOR`, then `vi` |
| `show_hidden` | bool | `false` | list dotfiles in the local file picker of `C` (copy files); the remote listing always shows them | `H` inside the local picker flips it and writes it back |

## Precedence

For every option: command-line flag, then environment variable, then this file, then
the default. A flag naming an unknown theme is an error; an unknown theme in the
environment or in the file falls through to the next source with a warning.

## Migration from the state file

Before this file existed, `t` persisted the theme to `~/.local/state/sasash/theme`.
`sasash setup` carries that choice into `config.toml` when it creates the file, and
the TUI still reads the old file only while `config.toml` does not exist. Once the
file is there, the state file is ignored and can be deleted.
