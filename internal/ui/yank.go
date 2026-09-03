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
// process runs in, whatever fd the TUI draws on.
func osc52Argv(text string) []string {
	payload := base64.StdEncoding.EncodeToString([]byte(text))
	return []string{"sh", "-c", `printf '\033]52;c;%s\a' "$1" > /dev/tty`, "sh", payload}
}

// clipTools are the fallbacks, in order; the first one on PATH takes
// the text. xclip needs the clipboard selection named.
const clipTools = `for t in pbcopy wl-copy xclip clip.exe; do
  if command -v "$t" >/dev/null 2>&1; then
    case "$t" in xclip) printf %s "$1" | xclip -selection clipboard ;; *) printf %s "$1" | "$t" ;; esac || exit $?
    printf %s "$t"; exit 0
  fi
done
exit 3`

// clipToolArgv runs the detection script: stdout names the tool that
// took the text; exit 3 means none was found.
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
			why = append(why, "terminal: "+oneLineText(string(res.Stderr)))
		default:
			via = append(via, "terminal (OSC 52)")
		}
		ctx, cancel = context.WithTimeout(context.Background(), clipTimeout)
		res, err = r.Run(ctx, clipToolArgv(text)...)
		cancel()
		switch {
		case err != nil:
			why = append(why, "tools: "+err.Error())
		case res.Code == 3:
			why = append(why, "no clipboard tool on PATH (pbcopy, wl-copy, xclip, clip.exe)")
		case !res.OK():
			why = append(why, "tool: "+oneLineText(string(res.Stderr)))
		default:
			via = append(via, strings.TrimSpace(string(res.Stdout)))
		}
		if len(via) == 0 {
			return copyDoneMsg{value: text, err: fmt.Errorf("%s", strings.Join(why, "; "))}
		}
		return copyDoneMsg{value: text, via: via}
	}
}

// copied is the yank's outcome on the two tiers: a one-liner naming what
// ran, or — the floor — a notice that carries the value and a popup that
// shows it whole with the reasons.
func (m Model) copied(msg copyDoneMsg) Model {
	if msg.err == nil {
		m.notice = "copied via " + strings.Join(msg.via, " + ")
		return m
	}
	m.notice = "clipboard not available — " + msg.value
	return m.show("clipboard not available here", "the value, to copy off the screen:", "  "+msg.value, "", "tried: "+msg.err.Error())
}
