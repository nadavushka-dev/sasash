package ui

import (
	"context"
	"encoding/base64"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/run"
)

// Yank (SASASH.6): the clipboard goes OSC 52 FIRST — the terminal's own
// clipboard escape, which works on macOS, Linux and WSL terminals and
// over ssh alike — then a detected tool behind it (pbcopy, wl-copy,
// xclip, clip.exe). Both run through the run seam as exact argv: the
// text travels as a positional argument, never spliced into a script.
// The notice names what ran. When nothing can take the text — no
// terminal to write the escape to and no tool — that is never a silent
// no-op: the notice says so and carries the value itself, and the
// attention popup shows it whole, so it can still be copied off the
// screen.

// clipTimeout bounds a clipboard write: a wedged pasteboard server must
// surface as a failure, not as silence.
const clipTimeout = 2 * time.Second

// osc52Argv writes the OSC 52 sequence for text to the controlling
// terminal. The payload is base64 (the escape's own encoding), built in
// Go so the shell never sees the text; /dev/tty is the terminal this
// process runs in, whatever fd the TUI draws on. The write races the
// renderer's frames on the same terminal: one small write of an
// invisible escape, worst case a glitched frame the next paint fixes —
// bubbletea exposes no clipboard write of its own.
func osc52Argv(text string) []string {
	payload := base64.StdEncoding.EncodeToString([]byte(text))
	return []string{"sh", "-c", `printf '\033]52;c;%s\a' "$1" > /dev/tty`, "sh", payload}
}

// clipTools are the fallbacks, in order; the first one on PATH that
// TAKES the text wins (a wl-copy on an X11 desktop fails, and xclip is
// next). Each tool runs with its stdio detached: xclip forks a
// selection owner that would otherwise hold the seam's stdout pipe open
// until the next paste, past any timeout. So the tool's own words are
// not captured; its exit status is, and the report names the tool and
// the status. stdout carries the outcome: the tool's name on success
// (exit 0), the failures on exit 98, nothing on exit 97 (no tool on
// PATH) — sentinels outside the range a tool would use itself.
const clipTools = `failed=
for t in pbcopy wl-copy xclip clip.exe; do
  command -v "$t" >/dev/null 2>&1 || continue
  if case "$t" in xclip) printf %s "$1" | xclip -selection clipboard ;; *) printf %s "$1" | "$t" ;; esac >/dev/null 2>&1; then
    printf %s "$t"; exit 0
  else
    failed="$failed${failed:+; }$t exited $?"
  fi
done
if [ -n "$failed" ]; then printf %s "$failed"; exit 98; fi
exit 97`

const (
	clipNoTool      = 97 // no tool on PATH
	clipToolsFailed = 98 // every tool found failed; stdout says how
)

// clipToolArgv runs the detection script through the run seam.
func clipToolArgv(text string) []string {
	return []string{"sh", "-c", clipTools, "sh", text}
}

// copyDoneMsg reports a yank: what took the text (terminal, a tool, or
// both), the value, and — when nothing did — why.
type copyDoneMsg struct {
	value string
	via   []string
	err   error
}

// clipCmd yanks text through the run seam, off the UI thread: the
// terminal escape first, then a tool; a failure of either alone is not a
// failure of the yank.
func clipCmd(r run.Runner, text string) tea.Cmd {
	return func() tea.Msg {
		var via, why []string
		ctx, cancel := context.WithTimeout(context.Background(), clipTimeout)
		res, err := r.Run(ctx, osc52Argv(text)...)
		cancel()
		switch {
		case err != nil:
			why = append(why, "terminal: "+err.Error())
		case !res.OK():
			why = append(why, "terminal: "+cleanLine(oneLineText(string(res.Stderr))))
		default:
			via = append(via, osc52Via)
		}
		ctx, cancel = context.WithTimeout(context.Background(), clipTimeout)
		res, err = r.Run(ctx, clipToolArgv(text)...)
		cancel()
		switch {
		case err != nil:
			why = append(why, "tools: "+err.Error())
		case res.Code == clipNoTool:
			why = append(why, "no clipboard tool on PATH (pbcopy, wl-copy, xclip, clip.exe)")
		case res.Code == clipToolsFailed:
			why = append(why, cleanLine(oneLineText(string(res.Stdout))))
		case !res.OK():
			why = append(why, fmt.Sprintf("tools script exited %d", res.Code))
		default:
			via = append(via, cleanLine(strings.TrimSpace(string(res.Stdout))))
		}
		if len(via) == 0 {
			return copyDoneMsg{value: text, err: fmt.Errorf("%s", strings.Join(why, "; "))}
		}
		return copyDoneMsg{value: text, via: via}
	}
}

// osc52Via names the terminal leg in the notice.
const osc52Via = "terminal (OSC 52)"

// copied is the yank's outcome on the two tiers: a one-liner naming what
// ran, or — the floor — a notice that carries the value and a popup that
// shows it whole with the reasons. The terminal leg cannot be confirmed
// (OSC 52 has no reply, and some terminals drop it), so when it is the
// only leg the line says so rather than promising a copy.
func (m Model) copied(msg copyDoneMsg) Model {
	if msg.err == nil {
		m.notice = "copied via " + strings.Join(msg.via, " + ")
		if len(msg.via) == 1 && msg.via[0] == osc52Via {
			m.notice += " only — unconfirmed; no clipboard tool here"
		}
		return m
	}
	// The value is config-influenced (a HostName, an actions.toml
	// template): cleaned on both tiers like the reasons are.
	v := cleanLine(msg.value)
	m.notice = "clipboard not available — " + v
	return m.show("clipboard not available here", "the value, to copy off the screen:", "  "+v, "", "tried: "+msg.err.Error())
}
