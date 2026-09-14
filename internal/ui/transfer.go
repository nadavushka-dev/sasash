package ui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/transfer"
)

// Copy files with scp (SASASH.4.33, marks 4.37): C on the selected host
// opens a three-step flow — the direction (upload local→host, download
// host→local), then the SOURCE picked in one browser, then the DEST
// directory picked in the other. Both ends are browsed by the same
// browser: rows with ../ on top, directories first with their trailing
// slash, a mark column. The local end lists through os.ReadDir (hidden
// entries follow config.toml's show_hidden, H flips it); the remote end
// lists through `ssh <alias> ls -1Ap -- <path>` run through the run
// seam — the trailing slash is the one fact it reads, never `ls -l`.
// In the source browser space marks a row and the marks are a basket
// that survives directory moves; enter with marks proceeds with them,
// enter without picks the highlighted row. One scp carries every
// source; -r is added when any source is a directory. The scp itself
// runs in the FOREGROUND exactly like the S handoff: the argv is shown
// whole in a confirm popup, enter hands the terminal to scp (native
// progress, any passphrase or host-key prompt), and the outcome is a
// notice on return — a non-zero exit is data, shown with scp's stderr,
// never a crash. scp and the listing name the alias only, so they ride
// the block's active HostName, its ProxyJump, and a live ControlMaster
// (SPEC §2, §4): nothing is resolved twice.

// xferStep is where the flow stands. The picker steps double as the
// end: the source is picked first, so srcPicked says whether the
// browser on screen chooses the source or the dest directory.
type xferStep int

const (
	xferOff       xferStep = iota
	xferDirection          // choose upload / download
	xferLocal              // the local browser holds the keys
	xferRemote             // the remote browser holds the keys
)

// mark is one picked source: its full path at its end, and whether it
// is a directory (then the copy is -r).
type mark struct {
	path  string
	isDir bool
}

// xferState is the flow's state: the alias, the direction, the marks
// while the source is being picked, the sources once picked, and the
// browser on screen.
type xferState struct {
	step   xferStep
	alias  string
	dir    transfer.Direction
	dirIdx int // the highlighted direction row while choosing

	marks     []mark // the basket while picking the source, in mark order
	srcs      []mark // the picked sources
	srcPicked bool

	br browser
}

func (x xferState) active() bool { return x.step != xferOff }

// sourceStep reports whether the browser on screen picks the source.
func (x xferState) sourceStep() bool { return !x.srcPicked && x.step >= xferLocal }

// marked reports whether path is in the basket.
func (x xferState) marked(path string) bool {
	for _, mk := range x.marks {
		if mk.path == path {
			return true
		}
	}
	return false
}

// browser is one end's picker: the listing of one directory, with a
// synthesized ../ row on top so the parent is always reachable.
// loading is the remote path being listed, "" when idle (a local
// listing is immediate).
type browser struct {
	path    string
	entries []transfer.Entry
	idx     int
	loading string
}

// rows are the browser's lines: ../ first, then the listing.
func (b browser) rows() []transfer.Entry {
	return append([]transfer.Entry{{Name: "..", IsDir: true}}, b.entries...)
}

// highlighted is the row under the cursor.
func (b browser) highlighted() transfer.Entry {
	rows := b.rows()
	if b.idx < 0 || b.idx >= len(rows) {
		return rows[0]
	}
	return rows[b.idx]
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

// showHiddenSavedMsg reports the outcome of persisting the H toggle.
type showHiddenSavedMsg struct{ err error }

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
	if x.step == xferDirection {
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
	}

	b := &x.br
	rows := b.rows()
	switch {
	case key.Matches(msg, m.keys.ClearFilter):
		return m.transferBack()
	case msg.String() == ".":
		if x.sourceStep() && len(x.marks) > 0 {
			m.notice = fmt.Sprintf("%d marked — enter copies them; space unmarks", len(x.marks))
			return m, nil
		}
		return m.transferChose([]mark{{path: b.path, isDir: true}})
	case msg.String() == " ":
		return m.transferToggleMark()
	case msg.String() == "H" && x.step == xferLocal:
		return m.transferToggleHidden()
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
	case key.Matches(msg, m.keys.FocusRight) || msg.String() == "right":
		// l opens a directory, nothing on a file.
		if row := b.highlighted(); row.IsDir {
			return m.transferOpen(row.Name)
		}
	case key.Matches(msg, m.keys.FocusLeft) || msg.String() == "backspace" || msg.String() == "left":
		return m.transferOpen("..")
	case msg.Type == tea.KeyEnter:
		if x.sourceStep() && len(x.marks) > 0 {
			return m.transferChose(x.marks)
		}
		row := b.highlighted()
		if row.Name == ".." {
			return m.transferOpen("..")
		}
		return m.transferChose([]mark{{path: m.transferChild(row.Name), isDir: row.IsDir}})
	}
	return m, nil
}

// transferToggleMark is space in the source browser: the highlighted
// row joins the basket or leaves it (../ is no source). In the dest
// browser it is a notice: the dest is one directory.
func (m Model) transferToggleMark() (Model, tea.Cmd) {
	x := &m.xfer
	if !x.sourceStep() {
		m.notice = "the destination is one directory — enter on it, or . for this one"
		return m, nil
	}
	row := x.br.highlighted()
	if row.Name == ".." {
		return m, nil
	}
	path := m.transferChild(row.Name)
	for i, mk := range x.marks {
		if mk.path == path {
			x.marks = append(x.marks[:i:i], x.marks[i+1:]...)
			return m, nil
		}
	}
	x.marks = append(x.marks, mark{path: path, isDir: row.IsDir})
	return m, nil
}

// transferChild is the full path of a row of the browser on screen, at
// its end: joined and cleaned the local way, or the way the host's
// shell reads it.
func (m Model) transferChild(name string) string {
	if m.xfer.step == xferRemote {
		return transfer.Descend(m.xfer.br.path, name)
	}
	return filepath.Join(m.xfer.br.path, name)
}

// transferOpen moves the browser into name ("..": the parent): the local
// browser lists at once, the remote one asks the host.
func (m Model) transferOpen(name string) (Model, tea.Cmd) {
	if m.xfer.step == xferRemote {
		next := transfer.Descend(m.xfer.br.path, name)
		if name == ".." {
			next = transfer.Parent(m.xfer.br.path)
		}
		return m.transferList(next)
	}
	return m.transferListLocal(m.transferChild(name)), nil
}

// transferPickSource opens the source browser for the chosen direction.
func (m Model) transferPickSource() (Model, tea.Cmd) {
	m.xfer.srcPicked, m.xfer.srcs = false, nil
	if m.xfer.dir == transfer.Download {
		return m.transferEnterRemote()
	}
	return m.transferEnterLocal(), nil
}

// transferBack is esc: one step back — the dest browser returns to the
// source browser (the marks kept, the pick undone), the source browser
// to the direction (the basket dropped), and that closes.
func (m Model) transferBack() (Model, tea.Cmd) {
	if m.xfer.srcPicked {
		return m.transferPickSource()
	}
	m.xfer = xferState{step: xferDirection, alias: m.xfer.alias, dirIdx: m.xfer.dirIdx}
	return m, nil
}

// transferEnterLocal opens the local browser in the user's home.
func (m Model) transferEnterLocal() Model {
	m.xfer.step = xferLocal
	dir := m.home
	if dir == "" {
		dir = "."
		if abs, err := filepath.Abs(dir); err == nil {
			dir = abs
		}
	}
	m.xfer.br = browser{path: dir}
	return m.transferListLocal(dir)
}

// transferListLocal reads dir into the browser; a directory that cannot
// be read is a notice and the browser stays where it was.
func (m Model) transferListLocal(dir string) Model {
	entries, err := readLocal(dir, m.showHidden)
	if err != nil {
		m.notice = "cannot list " + dir + ": " + cleanLine(err.Error())
		return m
	}
	m.xfer.br = browser{path: dir, entries: entries}
	return m
}

// readLocal lists dir the way the remote listing reads: directories
// first, each group in name order, dotfiles only when hidden is on. A
// symlink counts as what it points at — a link to a directory is a
// directory, enterable and copied -r, as scp follows it; a dangling
// link is a file.
func readLocal(dir string, hidden bool) ([]transfer.Entry, error) {
	des, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	var dirs, files []transfer.Entry
	for _, d := range des {
		name := d.Name()
		if !hidden && strings.HasPrefix(name, ".") {
			continue
		}
		isDir := d.IsDir()
		if d.Type()&os.ModeSymlink != 0 {
			if info, err := os.Stat(filepath.Join(dir, name)); err == nil {
				isDir = info.IsDir()
			}
		}
		if isDir {
			dirs = append(dirs, transfer.Entry{Name: name, IsDir: true})
		} else {
			files = append(files, transfer.Entry{Name: name})
		}
	}
	return append(dirs, files...), nil
}

// transferToggleHidden is H in the local browser: dotfiles on or off
// for the session, the directory re-read in place (the marks and the
// cursor stay), and the choice written back to config.toml's
// show_hidden (SASASH.4.36) the way t writes the theme. A directory
// that cannot be re-read flips nothing: the failure is the notice.
func (m Model) transferToggleHidden() (Model, tea.Cmd) {
	b := &m.xfer.br
	entries, err := readLocal(b.path, !m.showHidden)
	if err != nil {
		m.notice = "cannot list " + b.path + ": " + cleanLine(err.Error())
		return m, nil
	}
	m.showHidden = !m.showHidden
	name := b.highlighted().Name
	b.entries = entries
	b.idx = min(b.idx, len(b.rows())-1)
	for i, row := range b.rows() {
		if row.Name == name {
			b.idx = i // the cursor follows its row, wherever the dotfiles push it
			break
		}
	}
	m.notice = "hidden files: off"
	if m.showHidden {
		m.notice = "hidden files: on"
	}
	if m.saveShowHidden == nil {
		return m, nil
	}
	save, v := m.saveShowHidden, m.showHidden
	return m, func() tea.Msg { return showHiddenSavedMsg{err: save(v)} }
}

// transferEnterRemote opens the remote browser at the login home.
func (m Model) transferEnterRemote() (Model, tea.Cmd) {
	m.xfer.step = xferRemote
	m.xfer.br = browser{path: transfer.Home}
	return m.transferList(transfer.Home)
}

// listTimeout bounds one remote listing: a host that does not answer
// must not hold the browser on "listing …" past what a poll would wait.
const listTimeout = tunnelStatusTimeout

// transferList runs one listing through the run seam; the browser keeps
// showing where it is until the result lands.
func (m Model) transferList(path string) (Model, tea.Cmd) {
	m.xfer.br.loading = path
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
	b := &m.xfer.br
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

// transferChose is a pick landing: the sources first, then the dest
// directory — which opens the confirm with the exact scp line.
func (m Model) transferChose(picked []mark) (Model, tea.Cmd) {
	x := &m.xfer
	if !x.srcPicked {
		x.srcs, x.srcPicked = append([]mark(nil), picked...), true
		if x.dir == transfer.Download {
			return m.transferEnterLocal(), nil
		}
		return m.transferEnterRemote()
	}
	dest := picked[0]
	if !dest.isDir {
		m.notice = "the destination is a directory — enter on one, or . for this one"
		return m, nil
	}
	spec := m.transferSpec(dest.path)
	argv := strings.Join(transfer.CopyArgv(spec), " ")
	lines := []string{argv, ""}
	if len(spec.Sources) > 1 {
		lines = append(lines, fmt.Sprintf("%d sources, one scp run — it keeps going past one it cannot copy", len(spec.Sources)))
	}
	if spec.Recursive {
		lines = append(lines, "-r: a source is a directory, copied whole")
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

// transferSpec is the copy with dest as the picked directory: the
// sources in mark order, -r when any is a directory.
func (m Model) transferSpec(dest string) transfer.Spec {
	x := m.xfer
	spec := transfer.Spec{Alias: x.alias, Direction: x.dir, Dest: dest}
	for _, mk := range x.srcs {
		spec.Sources = append(spec.Sources, mk.path)
		spec.Recursive = spec.Recursive || mk.isDir
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
		what := strings.Join(msg.spec.SourceOperands(), " ")
		if n := len(msg.spec.Sources); n > 1 {
			what = fmt.Sprintf("%d items", n)
		}
		m.notice = "copied " + what + " → " + msg.spec.DestOperand()
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

// transferRoom is the rows the browser has under the panel's three lines.
func (m Model) transferRoom() int { return max(m.contentHeight()-3, 1) }

// transferPreview is the bottom line's argv while the flow is open: the
// scp exactly as it will run — the marks so far, or the end not yet
// picked as a placeholder — the honesty surface, never a
// runnable-looking guess.
func (m Model) transferPreview() []string {
	x := m.xfer
	if x.step == xferDirection {
		return nil
	}
	spec := transfer.Spec{Alias: x.alias, Direction: x.dir}
	srcs := x.srcs
	if !x.srcPicked {
		srcs = x.marks
	}
	for _, mk := range srcs {
		spec.Sources = append(spec.Sources, mk.path)
		spec.Recursive = spec.Recursive || mk.isDir
	}
	if len(spec.Sources) == 0 {
		spec.Sources = []string{"<local>"}
		if x.dir == transfer.Download {
			spec.Sources = []string{"<remote>"}
		}
	}
	spec.Dest = "<remote dir>"
	if x.dir == transfer.Download {
		spec.Dest = "<local dir>"
	}
	if !x.srcPicked {
		spec.Dest = strings.TrimSuffix(spec.Dest, " dir>") + ">"
	}
	return transfer.CopyArgv(spec)
}

// transferView renders the flow in the body: a title line, the role
// line, the current directory, then the browser.
func (m Model) transferView() string {
	x := m.xfer
	s := m.styles
	title := " " + s.SectionLabel.Render("copy files (scp) · ") + s.PathName.Render(x.alias)
	if x.step == xferDirection {
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
		return strings.TrimRight(b.String(), "\n")
	}
	where := x.br.path
	if x.step == xferRemote {
		where = x.alias + ":" + x.br.path
		if x.br.loading != "" {
			where = x.alias + ":" + x.br.loading + "  listing …"
		}
	}
	return title + "\n" + m.transferRoleLine() + "\n " + s.PathAddr.Render(where) + "\n" + m.browserView()
}

// transferRoleLine says what the browser on screen picks: the source
// (and how many are marked), or the dest directory for the sources.
func (m Model) transferRoleLine() string {
	x := m.xfer
	s := m.styles
	side := "local"
	if x.step == xferRemote {
		side = x.alias
	}
	if !x.srcPicked {
		how := "pick a file or directory on " + side + " (space marks several)"
		if n := len(x.marks); n > 0 {
			how = fmt.Sprintf("%d marked on %s — enter copies them, space unmarks", n, side)
		}
		return " " + s.SectionLabel.Render(x.dir.String()+" · source: ") + s.PathExtra.Render(how)
	}
	src := x.srcs[0].path
	if n := len(x.srcs); n > 1 {
		src = fmt.Sprintf("%d items", n)
	}
	return " " + s.SectionLabel.Render(x.dir.String()+" · source ") + s.PathName.Render(src) + s.SectionLabel.Render(" · dest: ") + s.PathExtra.Render("pick a directory on "+side+" (. for this one)")
}

// browserView renders the browser's window of rows: a mark column
// (✓, so a mark survives NO_COLOR), the cursor, the name — directories
// with their trailing slash, the ls -p glyph.
func (m Model) browserView() string {
	x := m.xfer
	b := x.br
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
		mk := " "
		if name != "../" && x.sourceStep() && x.marked(m.transferChild(rows[i].Name)) {
			mk = s.StatusUp.Render("✓")
		}
		if i == b.idx {
			out.WriteString(mk + " " + s.RowMark.Render("›") + " " + s.ItemSelected.Render(name) + "\n")
			continue
		}
		style := s.PathName
		if rows[i].IsDir {
			style = s.ActiveMark
		}
		out.WriteString(mk + "   " + style.Render(name) + "\n")
	}
	if len(b.entries) == 0 && b.loading == "" {
		out.WriteString("    " + s.Empty.Render("empty directory") + "\n")
	}
	return strings.TrimRight(out.String(), "\n")
}

// transferKeys is the footer while the flow holds the keys.
type transferKeys struct {
	step   xferStep
	source bool // the browser picks the source: space marks
}

func (t transferKeys) ShortHelp() []key.Binding {
	if t.step == xferDirection {
		return []key.Binding{
			key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel")),
			moveKeys,
			key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "pick")),
			key.NewBinding(key.WithKeys("u", "d"), key.WithHelp("u/d", "upload / download")),
		}
	}
	// No motion row here: the browser is visibly a list, the ? panel
	// treats j/k as self-evident, and with it the source browser's row
	// would outgrow 80 columns — where bubbles' help cuts the tail
	// mid-word rather than with its ellipsis. Every row that fits is a
	// key that does something only here.
	keys := []key.Binding{
		key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "back")),
		key.NewBinding(key.WithKeys("enter"), key.WithHelp("enter", "pick")),
		key.NewBinding(key.WithKeys("."), key.WithHelp(".", "this dir")),
	}
	if t.source {
		keys = append(keys, key.NewBinding(key.WithKeys(" "), key.WithHelp("space", "mark")))
	}
	if t.step == xferLocal {
		// The remote listing (ls -A) always shows dotfiles; only the
		// local browser has a switch.
		keys = append(keys, key.NewBinding(key.WithKeys("H"), key.WithHelp("H", "dotfiles")))
	}
	return append(keys,
		key.NewBinding(key.WithKeys("l"), key.WithHelp("l", "open")),
		key.NewBinding(key.WithKeys("h"), key.WithHelp("h", "up")),
	)
}

func (t transferKeys) FullHelp() [][]key.Binding { return [][]key.Binding{t.ShortHelp()} }

// transferHelpKeys is the keys panel's group: C and the keys that mean
// something only here — j/k and enter are the panel's usual rows, and
// esc's two verbs are one row (the footer says which applies, as the
// prompts group has it).
func transferHelpKeys(k KeyMap) []key.Binding {
	out := []key.Binding{k.Copy}
	for _, t := range []transferKeys{{step: xferDirection}, {step: xferLocal, source: true}} {
		for _, b := range t.ShortHelp() {
			switch b.Help().Key {
			case "u/d", "l", "h", ".", "space", "H":
				out = append(out, b)
			}
		}
	}
	return append(out, key.NewBinding(key.WithKeys("esc"), key.WithHelp("esc", "cancel / back — the footer says which")))
}
