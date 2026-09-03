package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Two-tier feedback (SASASH.4.11). Routine one-liners stay on the bottom
// line as before: a theme saved, a copy done, a single-path no-op.
// Attention-worthy content — an ssh or action failure with its whole
// reason, a reload with several warnings, a spawn that failed with the
// argv it tried, a corrupt cache — goes to this popup: a dismissable,
// palette-styled, width-clamped box with multi-line content, on the
// settings-panel pattern (it owns the keyboard; esc or enter closes;
// action keys are inert). One popup at a time, newest wins and the one
// it replaced is named; probes and status keep landing behind it.

// popup is the open box, zero when none.
type popup struct {
	title  string
	lines  []string
	prior  string // the title this one replaced, "" when none
	offset int    // first visible line when the content outgrows the frame
}

func (p popup) open() bool { return p.title != "" }

// show opens a popup, replacing an open one and saying so.
func (m Model) show(title string, lines ...string) Model {
	prior := ""
	if m.popup.open() {
		prior = m.popup.title
	}
	m.popup = popup{title: title, lines: lines, prior: prior}
	return m
}

// popupKey routes a key while the popup owns the keyboard: esc and enter
// close, j/k and the half pages scroll, everything else is inert.
func (m Model) popupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.ClearFilter) || msg.Type == tea.KeyEnter {
		m.popup = popup{}
		return m, nil
	}
	p := &m.popup
	switch {
	case key.Matches(msg, m.keys.Down):
		p.offset++
	case key.Matches(msg, m.keys.Up):
		p.offset--
	case key.Matches(msg, m.keys.HalfDown):
		p.offset += m.halfPage()
	case key.Matches(msg, m.keys.HalfUp):
		p.offset -= m.halfPage()
	case key.Matches(msg, m.keys.Top):
		p.offset = 0
	case key.Matches(msg, m.keys.Bottom):
		p.offset = len(m.popupBody())
	}
	// Clamped where it is written, not only where it is read: an offset
	// past the last legal value would eat the next k presses silently.
	p.offset = min(max(p.offset, 0), max(len(m.popupBody())-m.popupRoom(), 0))
	return m, nil
}

// popupRoom is how many content rows the frame shows: the title, a blank
// and the hint take three.
func (m Model) popupRoom() int { return max(m.contentHeight()-3, 1) }

// popupBody is the content as lines wrapped to the box width: nothing
// is cut, a long reason wraps — down to a width of one on an absurdly
// narrow frame, ugly but whole.
func (m Model) popupBody() []string {
	w := max(m.width-6, 1)
	wrap := lipgloss.NewStyle().Width(w)
	var out []string
	if m.popup.prior != "" {
		out = append(out, m.styles.PathExtra.Render("(replaced: "+m.popup.prior+")"))
	}
	for _, l := range m.popup.lines {
		out = append(out, strings.Split(wrap.Render(l), "\n")...)
	}
	return out
}

// popupView renders the box in the body's place: the title, the content
// from the scroll offset, and a scroll hint when there is more.
func (m Model) popupView() string {
	body := m.popupBody()
	room := m.popupRoom()
	p := m.popup
	offset := min(max(p.offset, 0), max(len(body)-room, 0))
	var b strings.Builder
	b.WriteString(" " + m.styles.Notice.Render("! "+p.title))
	b.WriteString("\n")
	end := min(offset+room, len(body))
	for _, l := range body[offset:end] {
		b.WriteString("\n   " + l)
	}
	if len(body) > room {
		b.WriteString("\n " + m.styles.PathExtra.Render(scrollHint(offset, end, len(body))))
	}
	return b.String()
}

func scrollHint(from, to, total int) string {
	return fmt.Sprintf("lines %d–%d of %d · j/k scroll", from+1, to, total)
}

// popupKeys is the help shown while the popup owns the keyboard.
type popupKeys struct{}

func (popupKeys) ShortHelp() []key.Binding {
	return []key.Binding{
		key.NewBinding(key.WithKeys("esc", "enter"), key.WithHelp("esc/enter", "close")),
		key.NewBinding(key.WithKeys("j", "k"), key.WithHelp("j/k", "scroll")),
	}
}

func (k popupKeys) FullHelp() [][]key.Binding { return [][]key.Binding{k.ShortHelp()} }

// reasonLines splits an error's text into lines for the popup, the
// argv-prefixed form the engine uses ("<argv>: <stderr>") kept whole.
// stderr is remote-influenced text (a banner, a ProxyCommand): control
// characters are dropped so a stray CR or escape cannot redraw the
// frame; tabs become spaces.
func reasonLines(err error) []string {
	var out []string
	for _, l := range strings.Split(err.Error(), "\n") {
		if l = cleanLine(l); l != "" {
			out = append(out, l)
		}
	}
	return out
}

// cleanLine drops control characters from one line of remote- or
// file-influenced text (tabs become spaces) and trims its end: ssh's
// stderr, a tool's reply and a config warning pass through it on both
// tiers, so none of them can redraw the frame. (A Go exec error naming
// the user's own argv is not that kind of text and is shown as is.)
func cleanLine(l string) string {
	l = strings.Map(func(r rune) rune {
		switch {
		case r == '\t':
			return ' '
		case r < 0x20 || r == 0x7f:
			return -1
		}
		return r
	}, l)
	return strings.TrimRight(l, " ")
}

// corruptCaches opens the popup the first time a host's status reports
// an unreadable cache: what it means and where the file lives. The pane
// keeps its one-line marker on every poll; the popup does not repeat.
func (m Model) corruptCaches(msg tunnelStatusMsg) Model {
	if msg.err != nil || m.corruptSeen[msg.host] {
		return m
	}
	st := msg.status
	if !st.CorruptState && !st.CorruptStats {
		return m
	}
	m.corruptSeen[msg.host] = true
	// The one-line form, so the bottom line and the popup agree and the
	// outcome stays on the line after the popup closes.
	m.notice = "cache unreadable for " + msg.host + " — see the tunnels pane"
	where := "the state directory"
	if m.stateDir != "" {
		where = m.stateDir
	}
	var lines []string
	if st.CorruptState {
		lines = append(lines, "tunnel state cache unreadable — treated as empty: remembered ad-hoc forwards are forgotten until re-added", "file: "+msg.host+".json under "+where)
	}
	if st.CorruptStats {
		lines = append(lines, "stats cache unreadable — up/down times start fresh from now", "file: "+msg.host+".stats.json under "+where)
	}
	lines = append(lines, "delete the file to reset it; sasash never rewrites a file it cannot read")
	return m.show("cache unreadable for "+msg.host, lines...)
}
