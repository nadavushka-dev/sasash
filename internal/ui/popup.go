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
// palette-styled box centered in the body (SASASH.4.14: a border, so it
// reads as a popup with or without color), width- and height-clamped
// with scroll, on the settings-panel pattern (it owns the keyboard; esc
// or enter closes; action keys are inert). One popup at a time, newest
// wins and the one it replaced is named; probes and status keep landing
// behind it. `/` narrows its rows vim-style (see finder).

// popup is the open box, zero when none.
type popup struct {
	title  string
	lines  []popupLine
	prior  string // the title this one replaced, "" when none
	offset int    // first visible line when the content outgrows the frame
	glyph  string // "!" for attention (the default), "?" for the keys panel
	find   finder // the / search over the rows
}

// popupLine is one row: the text as drawn and the words the / search
// sees (styling carries escape codes a substring match must not see).
// A heading names the group below it and is kept while any of its rows
// matches; a blank separates groups.
type popupLine struct {
	text, plain string
	heading     bool
}

func (p popup) open() bool { return p.title != "" }

// finder is the vim-style / search on a popup's or the settings menu's
// rows (SASASH.4.14): / starts typing, every character narrows the rows
// to those containing the query (case-insensitive, a substring — vim's
// /), backspace widens, esc clears back to the full list, enter keeps
// the narrowed rows and hands the keys back (the surface says what enter
// then does: the keys panel scrolls them, the settings menu activates
// the highlighted one). No n/N: at these row counts the narrowing IS the
// match list, so a next-match key would add a mode and nothing else.
type finder struct {
	query  string
	typing bool
}

// key consumes a key while typing — text, backspace, esc, enter — and
// reports whether it did; anything else (an arrow, a half page) is the
// caller's, so the list can be moved through without leaving the search.
func (f *finder) key(msg tea.KeyMsg) bool {
	if !f.typing {
		return false
	}
	switch msg.Type {
	case tea.KeyEsc:
		*f = finder{}
	case tea.KeyEnter:
		f.typing = false
	case tea.KeyBackspace:
		if r := []rune(f.query); len(r) > 0 {
			f.query = string(r[:len(r)-1])
		}
	case tea.KeySpace:
		f.query += " "
	case tea.KeyRunes:
		// A bracketed paste arrives as one KeyRunes with the raw bytes:
		// control characters go (a newline would break the box's row
		// count, an escape would redraw the frame); a space stays.
		f.query += strings.Map(func(r rune) rune {
			if r < 0x20 || r == 0x7f {
				return -1
			}
			return r
		}, string(msg.Runes))
	default:
		return false
	}
	return true
}

// matches reports whether a row's words hold the query.
func (f finder) matches(plain string) bool {
	return strings.Contains(strings.ToLower(plain), strings.ToLower(f.query))
}

// hint is the search as the box's bottom line shows it: the query with
// a cursor while typing, bare once kept, nothing when off.
func (f finder) hint() string {
	switch {
	case f.typing:
		return "/" + f.query + "▏"
	case f.query != "":
		return "/" + f.query
	}
	return ""
}

// show opens a popup, replacing an open one and saying so.
func (m Model) show(title string, lines ...string) Model {
	rows := make([]popupLine, len(lines))
	for i, l := range lines {
		rows[i] = popupLine{text: l, plain: l}
	}
	return m.showRows(title, rows)
}

// showRows is show for rows that carry their own plain words.
func (m Model) showRows(title string, rows []popupLine) Model {
	prior := ""
	if m.popup.open() {
		prior = m.popup.title
	}
	m.popup = popup{title: title, lines: rows, prior: prior}
	return m
}

// popupKey routes a key while the popup owns the keyboard: esc and enter
// close, j/k and the half pages scroll, / searches, everything else is
// inert.
func (m Model) popupKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	p := &m.popup
	if p.find.key(msg) {
		p.offset = 0 // the rows changed under the window
		return m, nil
	}
	if key.Matches(msg, m.keys.Filter) {
		p.find, p.offset = finder{typing: true}, 0
		return m, nil
	}
	// esc clears a kept search before it closes anything.
	if key.Matches(msg, m.keys.ClearFilter) && p.find.query != "" {
		p.find, p.offset = finder{}, 0
		return m, nil
	}
	// The keys panel is a reference, not an acknowledgement: ? and q
	// close it too.
	if key.Matches(msg, m.keys.ClearFilter) || msg.Type == tea.KeyEnter || (p.title == helpTitle && (key.Matches(msg, m.keys.Help) || key.Matches(msg, m.keys.Quit))) {
		m.popup = popup{}
		return m, nil
	}
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

// popupRoom is how many content rows the box shows: the border takes
// two, the title, a blank and the hint three more.
func (m Model) popupRoom() int { return max(m.contentHeight()-boxChrome, 1) }

// boxChrome is the rows a box spends around its content: two border
// rows, the title, the blank under it, the hint line.
const boxChrome = 5

// popupBody is the content as lines wrapped to the box width: nothing
// is cut, a long reason wraps — down to a width of one on an absurdly
// narrow frame, ugly but whole. A search narrows the rows first; no
// match is said, not shown as an empty box.
func (m Model) popupBody() []string {
	var out []string
	if m.popup.prior != "" {
		out = append(out, m.styles.PathExtra.Render("(replaced: "+m.popup.prior+")"))
	}
	rows := m.popup.visible()
	if len(rows) == 0 && m.popup.find.query != "" {
		return append(out, m.styles.Empty.Render(noMatch(m.popup.find.query)))
	}
	// Width wraps AND pads; the padding goes, so the box is as wide as
	// its longest row, not the frame, and can float centered.
	wrap := lipgloss.NewStyle().Width(m.boxWidth())
	for _, l := range rows {
		for _, w := range strings.Split(wrap.Render(l.text), "\n") {
			out = append(out, strings.TrimRight(w, " "))
		}
	}
	return out
}

// noMatch is the empty result of a search, with the way out.
func noMatch(query string) string { return "no match for /" + query + " — esc clears" }

// visible is the rows the search leaves: with no query, all of them;
// otherwise the matching rows under their headings, groups still set
// apart by a blank, headings and blanks never matched themselves.
func (p popup) visible() []popupLine {
	if p.find.query == "" {
		return p.lines
	}
	var out, group []popupLine
	flush := func() {
		var head *popupLine
		var rows []popupLine
		for i := range group {
			l := group[i]
			switch {
			case l.heading:
				head = &l
			case l.text != "" && p.find.matches(l.plain):
				rows = append(rows, l)
			}
		}
		if len(rows) > 0 {
			if len(out) > 0 {
				out = append(out, popupLine{})
			}
			if head != nil {
				out = append(out, *head)
			}
			out = append(out, rows...)
		}
		group = nil
	}
	for _, l := range p.lines {
		if l.heading {
			flush()
		}
		group = append(group, l)
	}
	flush()
	return out
}

// boxWidth is the content width inside the box: the frame less the
// border, the row indent and the right padding.
func (m Model) boxWidth() int { return max(m.width-6, 1) }

// popupView renders the box: the title, the content from the scroll
// offset, and a bottom line carrying the search and a scroll hint when
// there is more.
func (m Model) popupView() string {
	body := m.popupBody()
	room := m.popupRoom()
	p := m.popup
	offset := min(max(p.offset, 0), max(len(body)-room, 0))
	glyph, title := p.glyph, m.styles.Notice
	if glyph == "" {
		glyph = "!"
	} else {
		title = m.styles.SectionLabel // a reference, not a warning
	}
	end := min(offset+room, len(body))
	var hint []string
	if h := p.find.hint(); h != "" {
		hint = append(hint, m.styles.CmdPreview.Render(h))
	}
	if len(body) > room {
		hint = append(hint, m.styles.PathExtra.Render(scrollHint(offset, end, len(body))))
	}
	return m.box(title.Render(glyph+" "+p.title), body[offset:end], strings.Join(hint, helpSeparator))
}

// box draws the popup frame the attention popup, the keys panel and the
// settings menu share: a bordered, palette-styled box — the title, a
// blank, the rows indented, and a bottom line when there is one — that
// the view centers in the body. The title is clamped to the content
// width so a long one cannot widen the box past the frame; on a frame
// too short for even one row under the chrome, the blank goes first,
// then the border.
func (m Model) box(title string, rows []string, bottom string) string {
	w := m.boxWidth()
	var b strings.Builder
	b.WriteString(m.styles.Clamp.MaxWidth(w + 2).Render(" " + title))
	tall := 3 + len(rows) // the border and the title
	if bottom != "" {
		tall++
	}
	if tall < m.contentHeight() {
		b.WriteString("\n")
	}
	for _, l := range rows {
		b.WriteString("\n   " + l)
	}
	if bottom != "" {
		b.WriteString("\n " + m.styles.Clamp.MaxWidth(w+2).Render(bottom))
	}
	style := m.styles.Popup
	if m.contentHeight() < boxChrome {
		// Too short for the border to leave a row: the panel goes bare
		// rather than showing a frame around nothing.
		style = style.BorderTop(false).BorderBottom(false).BorderLeft(false).BorderRight(false)
	}
	return style.Render(b.String())
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
		key.NewBinding(key.WithKeys("/"), key.WithHelp("/", "search")),
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
