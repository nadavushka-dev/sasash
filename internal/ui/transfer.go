package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/charmbracelet/bubbles/filepicker"
	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"github.com/nadavushka-dev/sasash/internal/transfer"
)

// Copy files with scp (SASASH.4.33): C on the selected host opens a
// three-step flow — the direction (upload local→host, download
// host→local), then the SOURCE picked in one browser, then the DEST
// directory picked in the other. Both ends are browsed: the local end is
// the bubbles filepicker (a file or a directory is selectable), the
// remote end a browser over `ssh <alias> ls -1Ap -- <path>` run through
// the run seam — the trailing slash is the one fact it reads, never
// `ls -l`. A directory source makes the copy -r. The scp itself runs in
// the FOREGROUND exactly like the S handoff: the argv is shown whole in
// a confirm popup, enter hands the terminal to scp (native progress,
// any passphrase or host-key prompt), and the outcome is a notice on
// return — a non-zero exit is data, shown with scp's stderr, never a
// crash. scp and the listing name the alias only, so they ride the
// block's active HostName, its ProxyJump, and a live ControlMaster
// (SPEC §2, §4): nothing is resolved twice.

// xferStep is where the flow stands. The picker steps double as the
// role: the source is picked first, so srcPicked says whether the
// picker on screen chooses the source or the dest directory.
type xferStep int

const (
	xferOff       xferStep = iota
	xferDirection          // choose upload / download
	xferLocal              // the filepicker holds the keys
	xferRemote             // the remote browser holds the keys
)

// xferState is the flow's state: the alias, the direction, the source
// once picked, and the two browsers.
type xferState struct {
	step   xferStep
	alias  string
	dir    transfer.Direction
	dirIdx int // the highlighted direction row while choosing

	src       string
	srcDir    bool
	srcPicked bool

	local  filepicker.Model
	remote remoteBrowser
}

func (x xferState) active() bool { return x.step != xferOff }

// remoteBrowser is the host-side picker: the listing of one directory,
// with a synthesized ../ row on top so the parent is always reachable.
type remoteBrowser struct {
	path    string
	entries []transfer.Entry
	idx     int
	loading string // the path being listed, "" when idle
}

// rows are the browser's lines: ../ first, then the listing.
func (b remoteBrowser) rows() []transfer.Entry {
	return append([]transfer.Entry{{Name: "..", IsDir: true}}, b.entries...)
}

// remoteListMsg is one listing landing.
type remoteListMsg struct {
	alias, path string
	entries     []transfer.Entry
	err         error
}

// scpExitMsg is the terminal coming back from scp. err reports a failure
// to start or scp's exit status; stderr is what scp wrote there, kept
// for the outcome popup since the alt screen hides the run's own words.
type scpExitMsg struct {
	spec   transfer.Spec
	err    error
	stderr string
}

// scpExec is the ONE place the scp process is built, so a test can pin
// the args that reach the *exec.Cmd. Stdout stays on the terminal
// (scp's progress meter writes there); stderr is captured so a refusal
// can be shown after the TUI resumes, where scp's own line would be gone.
func scpExec(spec transfer.Spec) (*exec.Cmd, *bytes.Buffer) {
	cmd := execCommand(transfer.CopyArgv(spec))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	return cmd, &stderr
}

// openTransfer is C on the selected host.
func (m Model) openTransfer() (Model, tea.Cmd) {
	host, ok := m.Selected()
	if !ok {
		return m, nil
	}
	m.xfer = xferState{step: xferDirection, alias: host.Alias}
	return m, nil
}

// transferKey routes a key while the flow holds the keyboard.
func (m Model) transferKey(msg tea.KeyMsg) (Model, tea.Cmd) {
	x := &m.xfer
	switch x.step {
	case xferDirection:
		switch {
		case key.Matches(msg, m.keys.ClearFilter):
			m.xfer = xferState{}
			m.notice = "copy cancelled"
			return m, nil
		case key.Matches(msg, m.keys.Down) || key.Matches(msg, m.keys.Up) || msg.String() == "tab":
			x.dirIdx = 1 - x.dirIdx
			return m, nil
		case msg.String() == "u":
			x.dirIdx = 0
		case msg.String() == "d":
			x.dirIdx = 1
		case msg.Type == tea.KeyEnter:
		default:
			return m, nil
		}
		x.dir = transfer.Direction(x.dirIdx)
		return m.transferPickSource()

	case xferLocal:
		switch {
		case key.Matches(msg, m.keys.ClearFilter):
			return m.transferBack()
		case msg.String() == ".":
			return m.transferChose(x.local.CurrentDirectory, true)
		case msg.String() == "H":
			return m.transferToggleHidden()
		}
		var cmd tea.Cmd
		x.local, cmd = x.local.Update(msg)
		if ok, path := x.local.DidSelectFile(msg); ok {
			info, err := os.Stat(path)
			if err != nil {
				m.notice = "cannot read " + path + ": " + cleanLine(err.Error())
				return m, cmd
			}
			// enter on a directory selects AND descends in bubbles' picker:
			// its directory read rides along, so a declined confirm shows
			// the directory the path line names, not the parent's rows.
			next, pick := m.transferChose(path, info.IsDir())
			return next, tea.Batch(cmd, pick)
		}
		return m, cmd

	case xferRemote:
		b := &x.remote
		rows := b.rows()
		switch {
		case key.Matches(msg, m.keys.ClearFilter):
			return m.transferBack()
		case msg.String() == ".":
			return m.transferChose(b.path, true)
		case key.Matches(msg, m.keys.Down) || msg.String() == "tab":
			b.idx = min(b.idx+1, len(rows)-1)
		case key.Matches(msg, m.keys.Up) || msg.String() == "shift+tab":
			b.idx = max(b.idx-1, 0)
		case key.Matches(msg, m.keys.HalfDown):
			b.idx = min(b.idx+m.transferRoom()/2, len(rows)-1)
		case key.Matches(msg, m.keys.HalfUp):
			b.idx = max(b.idx-m.transferRoom()/2, 0)
		case key.Matches(msg, m.keys.Top):
			b.idx = 0
		case key.Matches(msg, m.keys.Bottom):
			b.idx = len(rows) - 1
		case key.Matches(msg, m.keys.FocusLeft) || msg.Type == tea.KeyBackspace:
			return m.transferList(transfer.Parent(b.path))
		case key.Matches(msg, m.keys.FocusRight) && msg.String() != "tab":
			if row := rows[b.idx]; row.IsDir {
				return m.transferList(transfer.Descend(b.path, row.Name))
			}
		case msg.Type == tea.KeyEnter:
			row := rows[b.idx]
			if row.Name == ".." {
				return m.transferList(transfer.Parent(b.path))
			}
			return m.transferChose(transfer.Descend(b.path, row.Name), row.IsDir)
		}
		return m, nil
	}
	return m, nil
}

// transferPickSource opens the source picker for the chosen direction.
func (m Model) transferPickSource() (Model, tea.Cmd) {
	m.xfer.srcPicked = false
	if m.xfer.dir == transfer.Download {
		return m.transferEnterRemote()
	}
	return m.transferEnterLocal()
}

// transferBack is esc: one step back — the dest picker returns to the
// source picker, the source picker to the direction, and that closes.
func (m Model) transferBack() (Model, tea.Cmd) {
	if m.xfer.srcPicked {
		return m.transferPickSource()
	}
	m.xfer = xferState{step: xferDirection, alias: m.xfer.alias, dirIdx: m.xfer.dirIdx}
	return m, nil
}

// showHiddenSavedMsg reports the outcome of persisting the H toggle.
type showHiddenSavedMsg struct{ err error }

// transferEnterLocal opens the filepicker in the user's home.
func (m Model) transferEnterLocal() (Model, tea.Cmd) {
	x := &m.xfer
	x.step = xferLocal
	dir := m.home
	if dir == "" {
		dir = "."
	}
	x.local = m.newLocalPicker(dir)
	return m, x.local.Init()
}

// transferToggleHidden is H in the local picker: dotfiles on or off for
// the session, written back to config.toml's show_hidden (SASASH.4.36)
// the way t writes the theme. The picker is rebuilt on its directory —
// bubbles' picker has no re-read of its own, and a fresh one ignores
// the old one's in-flight listing.
func (m Model) transferToggleHidden() (Model, tea.Cmd) {
	x := &m.xfer
	m.showHidden = !m.showHidden
	x.local = m.newLocalPicker(x.local.CurrentDirectory)
	m.notice = "hidden files: off"
	if m.showHidden {
		m.notice = "hidden files: on"
	}
	cmds := []tea.Cmd{x.local.Init()}
	if m.saveShowHidden != nil {
		save, v := m.saveShowHidden, m.showHidden
		cmds = append(cmds, func() tea.Msg { return showHiddenSavedMsg{err: save(v)} })
	}
	return m, tea.Batch(cmds...)
}

// newLocalPicker builds the local browser on dir with the flow's keys
// and the palette's styles; hidden entries follow show_hidden.
func (m Model) newLocalPicker(dir string) filepicker.Model {
	fp := filepicker.New()
	fp.CurrentDirectory = dir
	fp.DirAllowed, fp.FileAllowed = true, true
	fp.ShowPermissions, fp.ShowSize, fp.ShowHidden = true, true, m.showHidden
	fp.AutoHeight = false
	fp.Cursor = "›"
	fp.Styles = m.pickerStyles()
	// esc leaves the picker's keymap: it is the flow's way back, not a
	// directory up (h/backspace/left do that); ctrl+d/u page like the
	// list's.
	fp.KeyMap.Back = key.NewBinding(key.WithKeys("h", "backspace", "left"), key.WithHelp("h", "up"))
	fp.KeyMap.Open = key.NewBinding(key.WithKeys("l", "right", "enter"), key.WithHelp("l", "open"))
	fp.KeyMap.PageDown = key.NewBinding(key.WithKeys("J", "pgdown", "ctrl+d"), key.WithHelp("ctrl+d", "½ page down"))
	fp.KeyMap.PageUp = key.NewBinding(key.WithKeys("K", "pgup", "ctrl+u"), key.WithHelp("ctrl+u", "½ page up"))
	fp.SetHeight(m.transferRoom())
	return fp
}

// transferEnterRemote opens the remote browser at the login home.
func (m Model) transferEnterRemote() (Model, tea.Cmd) {
	m.xfer.step = xferRemote
	m.xfer.remote = remoteBrowser{path: transfer.Home}
	return m.transferList(transfer.Home)
}

// listTimeout bounds one remote listing: a host that does not answer
// must not hold the browser on "listing …" past what a poll would wait.
const listTimeout = tunnelStatusTimeout

// transferList runs one listing through the run seam; the browser keeps
// showing where it is until the result lands.
func (m Model) transferList(path string) (Model, tea.Cmd) {
	m.xfer.remote.loading = path
	alias, runner := m.xfer.alias, m.runner
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), listTimeout)
		defer cancel()
		entries, err := transfer.Lister{Runner: runner}.List(ctx, alias, path)
		return remoteListMsg{alias: alias, path: path, entries: entries, err: err}
	}
}

// remoteListed is a listing landing: the browser moves there, or stays
// and the line carries the host's refusal.
func (m Model) remoteListed(msg remoteListMsg) (Model, tea.Cmd) {
	b := &m.xfer.remote
	if m.xfer.step != xferRemote || msg.alias != m.xfer.alias || b.loading != msg.path {
		return m, nil // a listing for a browser no longer open
	}
	b.loading = ""
	if msg.err != nil {
		m.notice = m.xfer.alias + ":" + msg.path + " — " + cleanLine(msg.err.Error())
		// ssh's own exit (255) rather than ls's: the connection, not the
		// directory — under BatchMode that is a passphrase, a password or
		// a host key it could not ask for; a master answers all three.
		// The line points at S; the popup carries the argv and the words.
		var le *transfer.ListError
		if errors.As(msg.err, &le) && le.Code == 255 {
			m.notice = m.xfer.alias + ": " + cleanLine(msg.err.Error()) + " — S once (a master), then C"
			m = m.show("could not list "+m.xfer.alias+":"+msg.path,
				append(append([]string{strings.Join(transfer.ListArgv(m.xfer.alias, msg.path), " "), ""}, reasonLines(msg.err)...),
					"",
					"the listing runs behind the TUI with BatchMode, so ssh cannot ask for a",
					"passphrase, a password or a host-key answer here; connect once with S,",
					"and the master it opens carries the listing (and the copy) — then C again")...)
		}
		return m, nil
	}
	b.path, b.entries, b.idx = msg.path, msg.entries, 0
	return m, nil
}

// transferChose is a pick landing: the source first, then the dest
// directory — which opens the confirm with the exact scp line.
func (m Model) transferChose(path string, isDir bool) (Model, tea.Cmd) {
	x := &m.xfer
	if !x.srcPicked {
		x.src, x.srcDir, x.srcPicked = path, isDir, true
		if x.dir == transfer.Download {
			return m.transferEnterLocal()
		}
		return m.transferEnterRemote()
	}
	if !isDir {
		m.notice = "the destination is a directory — enter on one, or . for this one"
		return m, nil
	}
	spec := m.transferSpec(path)
	argv := strings.Join(transfer.CopyArgv(spec), " ")
	lines := []string{argv, ""}
	if spec.Recursive {
		lines = append(lines, "-r: the source is a directory, copied whole")
	}
	lines = append(lines,
		"runs in this terminal like S: scp's own progress and prompts,",
		"then back here with the outcome on the line")
	title := "copy files: upload to " + spec.Alias
	if spec.Direction == transfer.Download {
		title = "copy files: download from " + spec.Alias
	}
	m = m.ask(title, lines, "copy", "nothing copied", func(m Model) (Model, tea.Cmd) {
		m.xfer = xferState{}
		cmd, stderr := scpExec(spec)
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
			return scpExitMsg{spec: spec, err: err, stderr: stderr.String()}
		})
	})
	return m, nil
}

// transferSpec is the copy with dest as the picked directory.
func (m Model) transferSpec(dest string) transfer.Spec {
	x := m.xfer
	spec := transfer.Spec{Alias: x.alias, Direction: x.dir, Recursive: x.srcDir}
	if x.dir == transfer.Download {
		spec.Remote, spec.Local = x.src, dest
	} else {
		spec.Local, spec.Remote = x.src, dest
	}
	return spec
}

// scpReturned is the terminal coming back: the outcome on the line, the
// argv and scp's words in a popup when it refused.
func (m Model) scpReturned(msg scpExitMsg) (Model, tea.Cmd) {
	argv := strings.Join(transfer.CopyArgv(msg.spec), " ")
	var exitErr *exec.ExitError
	switch {
	case msg.err == nil:
		m.notice = "copied " + msg.spec.Source() + " → " + msg.spec.Dest()
	case errors.As(msg.err, &exitErr):
		reason := ""
		lines := strings.Split(strings.TrimSpace(msg.stderr), "\n")
		for _, l := range lines {
			if l = cleanLine(l); l != "" {
				reason = l
			}
		}
		if reason == "" {
			reason = fmt.Sprintf("exit status %d", exitErr.ExitCode())
		}
		m.notice = "scp: " + reason
		m = m.show(fmt.Sprintf("scp exited %d", exitErr.ExitCode()), append([]string{argv, ""}, lines...)...)
	default:
		m.notice = "scp: " + cleanLine(msg.err.Error())
	}
	return m, probeCmd(m.prober, m.hosts)
}

// transferRoom is the rows the picker has under the panel's three lines.
func (m Model) transferRoom() int { return max(m.contentHeight()-3, 1) }

// transferResize follows the terminal.
func (m Model) transferResize() Model {
	if m.xfer.step == xferLocal {
		m.xfer.local.SetHeight(m.transferRoom())
	}
	return m
}

// transferPreview is the bottom line's argv while the flow is open: the
// scp exactly as it will run, the end not yet picked as a placeholder —
// the honesty surface, never a runnable-looking guess.
func (m Model) transferPreview() []string {
	x := m.xfer
	if x.step == xferDirection {
		return nil
	}
	spec := transfer.Spec{Alias: x.alias, Direction: x.dir, Local: "<local>", Remote: "<remote>"}
	if x.srcPicked {
		spec.Recursive = x.srcDir
		if x.dir == transfer.Download {
			spec.Remote, spec.Local = x.src, "<local dir>"
		} else {
			spec.Local, spec.Remote = x.src, "<remote dir>"
		}
	}
	return transfer.CopyArgv(spec)
}

// transferView renders the flow in the body: a title line, the role
// line, the current directory, then the picker.
func (m Model) transferView() string {
	x := m.xfer
	s := m.styles
	title := " " + s.SectionLabel.Render("copy files (scp) · ") + s.PathName.Render(x.alias)
	switch x.step {
	case xferDirection:
		rows := []string{
			"upload    local → " + x.alias,
			"download  " + x.alias + " → local",
		}
		var b strings.Builder
		b.WriteString(title + "\n " + s.SectionLabel.Render("direction") + "\n\n")
		for i, r := range rows {
			if i == x.dirIdx {
				b.WriteString(" " + s.RowMark.Render("›") + " " + s.ItemSelected.Render(r) + "\n")
			} else {
				b.WriteString("   " + s.Item.Render(r) + "\n")
			}
		}
		return b.String()
	case xferLocal:
		role := m.transferRole("local")
		return title + "\n " + role + "\n " + s.PathAddr.Render(x.local.CurrentDirectory) + "\n" + x.local.View()
	case xferRemote:
		role := m.transferRole(x.alias)
		where := x.alias + ":" + x.remote.path
		if x.remote.loading != "" {
			where += "  " + s.StatusWait.Render("◌") + " " + s.PathExtra.Render("listing "+x.alias+":"+x.remote.loading+" …")
		}
		return title + "\n " + role + "\n " + s.PathAddr.Render(where) + "\n" + m.remoteView()
	}
	return ""
}

// transferRole says what the picker on screen chooses.
func (m Model) transferRole(side string) string {
	x := m.xfer
	s := m.styles
	if !x.srcPicked {
		return s.SectionLabel.Render(x.dir.String()+" · source: ") + s.PathExtra.Render("pick a file or directory on "+side)
	}
	src := x.src
	if x.srcDir {
		src += "/"
	}
	return s.SectionLabel.Render(x.dir.String()+" · source ") + s.PathName.Render(src) + s.SectionLabel.Render(" · dest: ") + s.PathExtra.Render("pick a directory on "+side+" (. for this one)")
}

// remoteView renders the browser's window of rows, the highlighted one
// marked, directories with their trailing slash (the ls -p glyph, so
// the kind survives NO_COLOR).
func (m Model) remoteView() string {
	b := m.xfer.remote
	s := m.styles
	rows := b.rows()
	room := m.transferRoom()
	start := 0
	if b.idx >= room {
		start = b.idx - room + 1
	}
	var out strings.Builder
	for i := start; i < len(rows) && i < start+room; i++ {
		name := rows[i].Name
		if rows[i].IsDir {
			name += "/"
		}
		if i == b.idx {
			out.WriteString(" " + s.RowMark.Render("›") + " " + s.ItemSelected.Render(name) + "\n")
			continue
		}
		style := s.PathName
		if rows[i].IsDir {
			style = s.ActiveMark
		}
		out.WriteString("   " + style.Render(name) + "\n")
	}
	if len(b.entries) == 0 && b.loading == "" {
		out.WriteString("   " + s.Empty.Render("empty directory") + "\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

// pickerStyles derives the filepicker's styles from the palette (SPEC
// §5 addendum: no color literal outside the palette layer).
func (m Model) pickerStyles() filepicker.Styles {
	s := m.styles
	return filepicker.Styles{
		DisabledCursor:   s.HelpDesc,
		Cursor:           s.RowMark,
		Symlink:          s.PathExtra,
		Directory:        s.ActiveMark,
		File:             s.PathName,
		DisabledFile:     s.PathExtra,
		Permission:       s.PathExtra,
		Selected:         s.ItemSelected,
		DisabledSelected: s.ItemSelectedDim,
		FileSize:         s.PathExtra.Width(7).Align(lipgloss.Right),
		EmptyDirectory:   s.Empty.PaddingLeft(3).SetString("empty directory"),
	}
}

// transferKeys is the footer while the flow holds the keys.
type transferKeys struct{ step xferStep }

func (t transferKeys) ShortHelp() []key.Binding {
	if t.step == xferDirection {
		return []key.Binding{
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
			moveKeys,
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "pick")),
			key.NewBinding(key.WithKeys("u", "d"), key.WithHelp("u/d", "upload / download")),
		}
	}
	keys := []key.Binding{
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		moveKeys,
		key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "open")),
		key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "up")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "select")),
		key.NewBinding(key.WithKeys("."), key.WithHelp(".", "this dir")),
	}
	if t.step == xferLocal {
		// The remote listing (ls -A) always shows dotfiles; only the
		// local picker has a switch.
		keys = append(keys, key.NewBinding(key.WithKeys("H"), key.WithHelp("H", "dotfiles")))
	}
	return keys
}

func (t transferKeys) FullHelp() [][]key.Binding { return [][]key.Binding{t.ShortHelp()} }

// transferHelpKeys is the keys panel's group: C and the keys that mean
// something only here — j/k and enter are the panel's usual rows, and
// esc's two verbs are one row (the footer says which applies, as the
// prompts group has it).
func transferHelpKeys(k KeyMap) []key.Binding {
	out := []key.Binding{k.Copy}
	for _, t := range []transferKeys{{step: xferDirection}, {step: xferLocal}} {
		for _, b := range t.ShortHelp() {
			switch b.Help().Key {
			case "u/d", "l", "h", ".", "H":
				out = append(out, b)
			}
		}
	}
	return append(out, key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel / back — the footer says which")))
}
