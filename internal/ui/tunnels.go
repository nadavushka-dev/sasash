package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

// Tunneler is the slice of tunnel.Engine the tunnels section consumes
// (CONTRACT-tunnel §3): status plus the compositions, never the raw
// command methods except Exit. The pane never builds ssh argv for
// execution — only for the preview line, pinned to the engine's own calls.
type Tunneler interface {
	Status(ctx context.Context, host string, desired []hostmodel.ForwardSet) (tunnel.HostStatus, error)
	Up(ctx context.Context, host, set string, specs []string) error
	DownSet(ctx context.Context, host, set string, specs []string) error
	Cancel(ctx context.Context, host, spec string) error
	Exit(ctx context.Context, host string) error
}

// tunnelStatusTimeout bounds one Status call: an `-O check` plus one local
// dial per forward, each already bounded by the engine's own timeout.
const tunnelStatusTimeout = 10 * time.Second

// tunnelActionTimeout bounds one action: `ssh -fN` may authenticate.
const tunnelActionTimeout = 60 * time.Second

// tunnelState is the last Status result for one host; err is a seam
// failure (the runner could not run ssh), never a "down".
type tunnelState struct {
	status tunnel.HostStatus
	err    error
}

// tunnelStatusMsg carries one host's Status result.
type tunnelStatusMsg struct {
	host   string
	status tunnel.HostStatus
	err    error
}

// tunnelDoneMsg reports the outcome of space / r / X. label names the
// target for a failure notice ("web", "master"); done is the success
// notice.
type tunnelDoneMsg struct {
	host, label, done string
	err               error
}

// Preview argv builders — the exact strings the engine runs (CONTRACT-
// tunnel §2). They exist only so the bottom line can show what space will
// do; TestTunnelArgv_MatchesEngine pins them to the engine's real calls.
func startArgv(host string) []string { return []string{"ssh", "-fN", host} }
func forwardArgv(host, spec string) []string {
	return []string{"ssh", "-O", "forward", "-L", spec, host}
}
func cancelArgv(host, spec string) []string { return []string{"ssh", "-O", "cancel", "-L", spec, host} }
func exitArgv(host string) []string         { return []string{"ssh", "-O", "exit", host} }

// tunnelStatusCmd polls one host's tunnel status off the UI thread; nil
// when the section is off or a poll for the host is already in flight —
// one `ssh -O check` per host at a time, so holding j across a fleet
// cannot fan out a process per keystroke, and results cannot land out of
// order.
func (m Model) tunnelStatusCmd(host hostmodel.Host) tea.Cmd {
	if m.tunnels == nil || m.tinflight[host.Alias] {
		return nil
	}
	m.tinflight[host.Alias] = true
	eng := m.tunnels
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), tunnelStatusTimeout)
		defer cancel()
		st, err := eng.Status(ctx, host.Alias, host.Forwards)
		return tunnelStatusMsg{host: host.Alias, status: st, err: err}
	}
}

// tunnelActionCmd runs one action off the UI thread.
func tunnelActionCmd(host, label, done string, act func(context.Context) error) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), tunnelActionTimeout)
		defer cancel()
		return tunnelDoneMsg{host: host, label: label, done: done, err: act(ctx)}
	}
}

// selectedStatusCmd polls the host under the cursor, if any.
func (m Model) selectedStatusCmd() tea.Cmd {
	host, ok := m.Selected()
	if !ok {
		return nil
	}
	return m.tunnelStatusCmd(host)
}

// hostByAlias finds a loaded host; ok is false after it was edited away.
func (m Model) hostByAlias(alias string) (hostmodel.Host, bool) {
	for _, h := range m.hosts {
		if h.Alias == alias {
			return h, true
		}
	}
	return hostmodel.Host{}, false
}

// forwardRows are the rows the tunnels section shows for host: the last
// Status result's forwards (shared-port claimants demoted), or — before
// one lands — the declared sets, flagged pending so they render as
// unknown rather than down.
func (m Model) forwardRows(host hostmodel.Host) (rows []tunnel.Forward, pending bool) {
	if ts, ok := m.tstatus[host.Alias]; ok && ts.err == nil {
		return demoteShared(ts.status.Forwards), false
	}
	for _, fs := range host.Forwards {
		for _, spec := range fs.Specs {
			rows = append(rows, tunnel.Forward{Set: fs.Name, Spec: spec})
		}
	}
	return rows, true
}

// demoteShared resolves the shared-local-port ambiguity (SASASH.3.4 added
// scope): the engine judges every spec by its local port alone, so a stale
// recorded spec riding a live desired spec's listener reads up. Only the
// first claimant of a port — desired sets come first, in meta order — can
// be the one answering; every later claimant is capped at listening, the
// contract's own word for "port answers, not verified end-to-end".
func demoteShared(rows []tunnel.Forward) []tunnel.Forward {
	out := append([]tunnel.Forward(nil), rows...)
	seen := make(map[string]bool)
	for i := range out {
		p := out[i].Port
		if p == "" {
			continue
		}
		if seen[p] && out[i].State == tunnel.Up {
			out[i].State = tunnel.Listening
		}
		seen[p] = true
	}
	return out
}

// sharedWith names the earlier claimant of row i's local port, or "".
func sharedWith(rows []tunnel.Forward, i int) string {
	for j := 0; j < i; j++ {
		if rows[i].Port != "" && rows[j].Port == rows[i].Port {
			return setLabel(rows[j].Set)
		}
	}
	return ""
}

// highlighted returns the forward row the tunnels focus is on, if any.
// The index is clamped lazily: a reload can shrink the rows under it.
func (m Model) highlighted(host hostmodel.Host) (tunnel.Forward, bool) {
	rows, _ := m.forwardRows(host)
	if m.fwdIdx < 0 || m.fwdIdx >= len(rows) {
		return tunnel.Forward{}, false
	}
	return rows[m.fwdIdx], true
}

// setSpecs gathers what the engine will act on for one set: the declared
// specs plus every row Status attributed to the set (recorded extras) —
// exactly the union DownSet cancels — and whether every row is Up.
func (m Model) setSpecs(host hostmodel.Host, set string) (specs []string, allUp bool) {
	seen := make(map[string]bool)
	add := func(s string) {
		if !seen[s] {
			seen[s] = true
			specs = append(specs, s)
		}
	}
	for _, fs := range host.Forwards {
		if fs.Name == set {
			for _, s := range fs.Specs {
				add(s)
			}
		}
	}
	rows, pending := m.forwardRows(host)
	allUp = !pending
	for _, f := range rows {
		if f.Set == set {
			add(f.Spec)
			if f.State != tunnel.Up {
				allUp = false
			}
		}
	}
	return specs, allUp
}

// setLabel names a set in notices and rows; the ad-hoc set has no name.
func setLabel(set string) string {
	if set == "" {
		return "ad-hoc"
	}
	return set
}

// actionPreview is what the bottom line shows: enter's ssh when no forward
// row is highlighted, otherwise the first ssh command space will run and
// how many follow it (a set is one -L per invocation). The engine's
// `-O check` before an Up is a probe, not an action, and is not counted.
func (m Model) actionPreview() (argv []string, more int) {
	host, ok := m.Selected()
	if !ok {
		return nil, 0
	}
	row, ok := m.highlighted(host)
	if !ok {
		return sshCommand(host.Alias), 0
	}
	specs, allUp := m.setSpecs(host, row.Set)
	if len(specs) == 0 {
		return sshCommand(host.Alias), 0
	}
	if allUp {
		return cancelArgv(host.Alias, specs[0]), len(specs) - 1
	}
	if ts, ok := m.tstatus[host.Alias]; !ok || !ts.status.Master {
		return startArgv(host.Alias), len(specs)
	}
	return forwardArgv(host.Alias, specs[0]), len(specs) - 1
}

// toggleCmd is space: bring the highlighted row's set up, or — when every
// forward of the set is already up — take it down. A partially-up set goes
// through Up; a forward that is already bound then fails with ssh's own
// message, which the notice shows rather than hides.
func (m Model) toggleCmd(host hostmodel.Host, row tunnel.Forward) tea.Cmd {
	eng, alias, set := m.tunnels, host.Alias, row.Set
	specs, allUp := m.setSpecs(host, set)
	name := setLabel(set)
	if allUp {
		return tunnelActionCmd(alias, name, name+" down", func(ctx context.Context) error {
			return eng.DownSet(ctx, alias, set, specs)
		})
	}
	return tunnelActionCmd(alias, name, name+" up", func(ctx context.Context) error {
		return eng.Up(ctx, alias, set, specs)
	})
}

// restartCmd is r, "restart master" (SPEC §5): exit the master and start
// a fresh one, nothing more — the forwards it carried are gone with it
// and come back per set through space, so nothing the user did not ask
// for gets bound. The exit's own failure is not news: a restart of a dead
// master is just a start, and anything that would stop a fresh master
// fails in Up.
func (m Model) restartCmd(host hostmodel.Host) tea.Cmd {
	eng, alias := m.tunnels, host.Alias
	return tunnelActionCmd(alias, "restart", "master restarted", func(ctx context.Context) error {
		_ = eng.Exit(ctx, alias)
		return eng.Up(ctx, alias, "", nil)
	})
}

// stateWriteOnly reports whether err is nothing but state-cache write
// failures: the ssh side succeeded. DownSet joins cancel failures and the
// cache failure into one error, so errors.Is alone would call a refused
// cancel a mere warning — every leaf has to be ErrStateWrite.
func stateWriteOnly(err error) bool {
	if joined, ok := err.(interface{ Unwrap() []error }); ok {
		for _, e := range joined.Unwrap() {
			if !stateWriteOnly(e) {
				return false
			}
		}
		return true
	}
	return errors.Is(err, tunnel.ErrStateWrite)
}

// oneLine folds a joined error onto the notice line: errors.Join
// separates with newlines, and a second line would push the frame off
// the terminal and truncate the message that matters. The engine echoes
// the argv ahead of ssh's reason ("ssh -O cancel -L … host: reason"); the
// user just read that argv on the preview line, so it is dropped here to
// keep the reason inside 80 columns.
func oneLine(err error, host string) string {
	lines := strings.Split(err.Error(), "\n")
	for i, line := range lines {
		if strings.HasPrefix(line, "ssh ") {
			if j := strings.Index(line, " "+host+": "); j >= 0 {
				lines[i] = line[j+len(host)+3:]
			}
		}
	}
	return strings.Join(lines, "; ")
}

// cancelCmd is x: `ssh -O cancel` for the one highlighted forward. A
// forward that is already gone fails with ssh's own reason.
func (m Model) cancelCmd(host hostmodel.Host, row tunnel.Forward) tea.Cmd {
	eng, alias, spec := m.tunnels, host.Alias, row.Spec
	return tunnelActionCmd(alias, "cancel", "cancelled "+spec, func(ctx context.Context) error {
		return eng.Cancel(ctx, alias, spec)
	})
}

// exitCmd is X: `ssh -O exit`, the master and every forward with it.
func (m Model) exitCmd(host hostmodel.Host) tea.Cmd {
	eng, alias := m.tunnels, host.Alias
	return tunnelActionCmd(alias, "exit", "master exited", func(ctx context.Context) error {
		return eng.Exit(ctx, alias)
	})
}

// tunnelsView renders the section under paths: the master, then one row
// per forward with its verbatim three-state status — glyph AND word, so
// the state survives NO_COLOR. Nothing is collapsed: listening is not up.
func (m Model) tunnelsView(host hostmodel.Host) string {
	var b strings.Builder
	b.WriteString("\n\n" + m.styles.SectionLabel.Render("tunnels"))

	ts, known := m.tstatus[host.Alias]
	glyph, word := m.styles.StatusWait.Render("◌"), "…"
	if known && ts.err == nil {
		glyph, word = m.styles.StatusDown.Render("○"), "down"
		if ts.status.Master {
			glyph, word = m.styles.StatusUp.Render("●"), "up"
		}
	}
	b.WriteString("\n   " + glyph + " " + m.styles.PathName.Render("master") + " " + m.styles.PathExtra.Render(word))
	if known && ts.err != nil {
		b.WriteString("\n   " + m.styles.Notice.Render("! status: "+ts.err.Error()))
		return b.String()
	}
	if ts.status.CorruptState {
		b.WriteString("\n   " + m.styles.Notice.Render("! state cache unreadable — treated as empty"))
	}

	rows, pending := m.forwardRows(host)
	nameWidth, specWidth := 0, 0
	for _, f := range rows {
		nameWidth = max(nameWidth, len(setLabel(f.Set)))
		specWidth = max(specWidth, len(f.Spec))
	}
	for i, f := range rows {
		mark := "  "
		if i == m.fwdIdx {
			mark = m.styles.RowMark.Render("› ")
		}
		glyph, word := m.styles.StatusWait.Render("◌"), "…"
		if !pending {
			switch f.State {
			case tunnel.Up:
				glyph = m.styles.StatusUp.Render("●")
			case tunnel.Listening:
				glyph = m.styles.StatusListening.Render("◐")
			default:
				glyph = m.styles.StatusDown.Render("○")
			}
			word = f.State.String()
		}
		b.WriteString(fmt.Sprintf("\n %s%s %s %s %s", mark, glyph,
			m.styles.PathName.Render(fmt.Sprintf("%-*s", nameWidth, setLabel(f.Set))),
			m.styles.PathAddr.Render(fmt.Sprintf("%-*s", specWidth, f.Spec)),
			m.styles.PathExtra.Render(word)))
		// A live later claimant cannot be told apart from the earlier
		// one on its port (demoteShared); a dead one has nothing to say.
		if !pending && f.State != tunnel.Down {
			if with := sharedWith(rows, i); with != "" {
				b.WriteString(" " + m.styles.Notice.Render("unverified: :"+f.Port+" claimed by "+with))
			}
		}
	}
	return b.String()
}
