package ui

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

// Tunneler is the slice of tunnel.Engine the tunnels section consumes
// (CONTRACT-tunnel, Model B): status, the per-set and per-forward
// spawn/kill, the host-wide kill, the interactive master's exit, and the
// record purge a re-plumb needs. The pane never builds ssh argv for
// execution — only for the preview line, from the engine's own builder.
type Tunneler interface {
	Status(ctx context.Context, host string, desired []hostmodel.ForwardSet) (tunnel.HostStatus, error)
	SpawnSet(ctx context.Context, host, set string, specs []string) error
	KillSet(ctx context.Context, host, set string, specs []string) error
	Spawn(ctx context.Context, host, set, spec string) error
	Kill(ctx context.Context, host, spec string) error
	// Adopt migrates a host's variant-keyed tunnel state into the host
	// (SASASH.4.23); tunnels belong to the machine, not the path-alias.
	Adopt(host string, variants []string) error
	Exit(ctx context.Context, host string) error
	Forget(host, spec string) error
}

// tunnelStatusTimeout bounds one Status call: an `-O check` for the
// interactive master plus one liveness check and one local dial per
// forward, each already bounded by the engine's own timeout.
const tunnelStatusTimeout = 10 * time.Second

// tunnelActionTimeout bounds one action: each spawned process may
// authenticate on its own.
const tunnelActionTimeout = 60 * time.Second

// tunnelState is the last Status result for one host; err is a seam
// failure (the runner could not run ssh), never a "down".
type tunnelState struct {
	status tunnel.HostStatus
	err    error
}

// tunnelStatusMsg carries one host's Status result. stampErr reports a
// RecordHandoff stamp that rode this poll and failed (best-effort).
type tunnelStatusMsg struct {
	host     string
	status   tunnel.HostStatus
	err      error
	stampErr error
}

// tunnelDoneMsg reports the outcome of space / r / X. label names the
// target for a failure notice ("web", "master"); done is the success
// notice.
type tunnelDoneMsg struct {
	host, label, done string
	err               error
}

// Preview builders — what the bottom line shows. A spawn is the engine's
// own argv (tunnel.SpawnArgv, the one owner of the literal); a kill is
// the SIGTERM the engine sends, written the way a shell would; the exit
// is the interactive master's `ssh -O exit`.
func spawnArgv(host, spec string) []string { return tunnel.SpawnArgv(host, spec) }
func killArgv(pid int) []string            { return []string{"kill", "-TERM", strconv.Itoa(pid)} }
func exitArgv(host string) []string        { return []string{"ssh", "-O", "exit", host} }

// tunnelStatusCmd polls one host's tunnel status off the UI thread; nil
// when the section is off or a poll for the host is already in flight —
// one `ssh -O check` per host at a time, so holding j across a fleet
// cannot fan out a process per keystroke, and results cannot land out of
// order.
func (m Model) tunnelStatusCmd(host hostmodel.Host) tea.Cmd {
	host = m.tunnelHost(host) // poll the machine, whichever alias is selected
	if m.tunnels == nil || m.tinflight[host.Alias] || m.switching[host.Alias] {
		return nil
	}
	m.tinflight[host.Alias] = true
	eng := m.tunnels
	// Migrate any variant-keyed tunnels into the parent BEFORE dispatching
	// the poll, synchronously on the UI thread (SASASH.4.23): Adopt and a
	// user write-action (SpawnSet/Kill) are both unlocked writers of
	// <parent>.json, and only the tinflight gate above serializes per host.
	// Running Adopt here — where the gate guarantees no action is in flight,
	// and the single-threaded Update guarantees none can begin mid-call —
	// keeps the migration off the poll goroutine that races those actions.
	// It writes nothing once there is nothing left to adopt, so the recurring
	// poll cost is a few stats. A write failure just leaves it for next time.
	_ = eng.Adopt(host.Alias, m.variantAliasesOf(host.Alias))
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), tunnelStatusTimeout)
		defer cancel()
		st, err := eng.Status(ctx, host.Alias, host.Forwards)
		return tunnelStatusMsg{host: host.Alias, status: st, err: err}
	}
}

// tunnelActionCmd runs one action off the UI thread. The host's
// in-flight gate is held for the action's duration: an action writes the
// state and stats caches, and the refresh tick must not poll — and
// observe/write — the same host meanwhile (one writer per host). The
// done arm releases the gate and re-polls.
func (m Model) tunnelActionCmd(host, label, done string, act func(context.Context) error) tea.Cmd {
	m.tinflight[host] = true
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
	host = m.tunnelHost(host) // tunnels belong to the machine, not the path-alias
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
// exactly the union KillSet ends — and whether every row is Up.
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
		return "unsaved"
	}
	return set
}

// actionPreview is what the bottom line shows: enter's ssh when no forward
// row is highlighted, otherwise the one command space will run on that
// row (SASASH.4.31: space toggles the highlighted forward, one process) —
// the kill of its process when it is up, else its spawn. more is what
// follows the shown command; a single forward has nothing following.
func (m Model) actionPreview() (argv []string, more int) {
	host, ok := m.Selected()
	if !ok {
		return nil, 0
	}
	row, ok := m.highlighted(host)
	if !ok {
		return m.enterPreview(host.Alias), 0
	}
	if row.State == tunnel.Up && row.Pid > 0 {
		return killArgv(row.Pid), 0
	}
	// The parent alias: the spawn runs against the machine (toggleCmd
	// canonicalizes), so the preview must not name the path-variant.
	return spawnArgv(m.tunnelHost(host).Alias, row.Spec), 0
}

// setPids lists the live pids of a set's up rows, in row order.
func (m Model) setPids(host hostmodel.Host, set string) []int {
	rows, _ := m.forwardRows(host)
	var pids []int
	for _, f := range rows {
		if f.Set == set && f.State == tunnel.Up && f.Pid > 0 {
			pids = append(pids, f.Pid)
		}
	}
	return pids
}

// notUp filters specs down to the ones SpawnSet will actually spawn: every
// spec whose row is not up. Before a poll lands nothing is known to be up,
// so every spec counts.
func (m Model) notUp(host hostmodel.Host, specs []string) []string {
	rows, pending := m.forwardRows(host)
	up := make(map[string]bool)
	if !pending {
		for _, f := range rows {
			if f.State == tunnel.Up {
				up[f.Spec] = true
			}
		}
	}
	var out []string
	for _, s := range specs {
		if !up[s] {
			out = append(out, s)
		}
	}
	return out
}

// enterPreview is what enter runs on the host row: the spawn plan when
// there is something to spawn into (a template that cannot expand shows
// its error on the line instead of a command), else the handoff's ssh.
func (m Model) enterPreview(alias string) []string {
	plan := m.spawn.planFor(alias)
	switch {
	case plan == nil:
		return sshCommand(alias)
	case plan.err != nil:
		return []string{"spawn:", plan.err.Error()}
	}
	return plan.preview()
}

// toggleCmd is space: toggle the highlighted forward alone (SASASH.4.31 —
// per-forward start and stop, symmetric). Up (a process of ours, its port
// answering) is killed; anything else is spawned — one process for that
// spec, the set's other forwards untouched. A port someone else holds
// (a listening row) comes back as the engine's refusal, which the notice
// shows rather than hides.
func (m Model) toggleCmd(host hostmodel.Host, row tunnel.Forward) tea.Cmd {
	host = m.tunnelHost(host)
	eng, alias, set, spec := m.tunnels, host.Alias, row.Set, row.Spec
	name := setLabel(set) // a failure is labelled by the set, as s's is; the argv in it names the spec
	if row.State == tunnel.Up {
		return m.tunnelActionCmd(alias, name, name+" "+spec+" down", func(ctx context.Context) error {
			return eng.Kill(ctx, alias, spec)
		})
	}
	return m.tunnelActionCmd(alias, name, name+" "+spec+" up", func(ctx context.Context) error {
		return eng.Spawn(ctx, alias, set, spec)
	})
}

// toggleSetCmd is s: bring the highlighted row's whole set up — one
// process per forward that is not already up, the up ones untouched — or,
// when every forward of the set is up, kill them all. A port someone else
// holds comes back as the engine's refusal, which the notice shows rather
// than hides.
func (m Model) toggleSetCmd(host hostmodel.Host, row tunnel.Forward) tea.Cmd {
	host = m.tunnelHost(host)
	eng, alias, set := m.tunnels, host.Alias, row.Set
	specs, allUp := m.setSpecs(host, set)
	name := setLabel(set)
	if allUp {
		return m.tunnelActionCmd(alias, name, name+" down", func(ctx context.Context) error {
			return eng.KillSet(ctx, alias, set, specs)
		})
	}
	return m.tunnelActionCmd(alias, name, name+" up", func(ctx context.Context) error {
		return eng.SpawnSet(ctx, alias, set, specs)
	})
}

// restartCmd is r, "restart master" (SPEC §5): exit the interactive
// master; ControlMaster auto starts a fresh one on the next connect, and
// the tunnels — their own processes — are not touched. ssh's refusal is
// the notice (a master that is not running says so), and a "No
// ControlPath" refusal is what opens the trio offer (SASASH.4.18).
func (m Model) restartCmd(host hostmodel.Host) tea.Cmd {
	host = m.tunnelHost(host)
	eng, alias := m.tunnels, host.Alias
	return m.tunnelActionCmd(alias, "restart", "master exited — a fresh one starts on the next connect", func(ctx context.Context) error {
		return eng.Exit(ctx, alias)
	})
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
				line = line[j+len(host)+3:]
			}
		}
		lines[i] = cleanLine(line)
	}
	return strings.Join(lines, "; ")
}

// cancelCmd is x: kill the one highlighted forward's process. The notice
// says what that does: the listener AND the connections established
// through it drop together (Model B — the clean kill). A row with no live
// process of ours has nothing to kill; the engine still retires its
// record (a named row) or keeps it as the ad-hoc declaration.
func (m Model) cancelCmd(host hostmodel.Host, row tunnel.Forward) tea.Cmd {
	host = m.tunnelHost(host)
	eng, alias, spec := m.tunnels, host.Alias, row.Spec
	done := "killed " + spec + " — its connections dropped with it"
	if row.Pid == 0 {
		done = spec + " has no process of ours to kill"
	}
	return m.tunnelActionCmd(alias, "kill", done, func(ctx context.Context) error {
		return eng.Kill(ctx, alias, spec)
	})
}

// tunnelsView renders the section under paths: the interactive master
// (an indicator for connection reuse, never a forward's verdict), then
// one row per forward with its verbatim three-state status — glyph AND
// word, so the state survives NO_COLOR. Nothing is collapsed: listening
// (the port answers, but no process of ours) is not up.
func (m Model) tunnelsView(host hostmodel.Host) string {
	host = m.tunnelHost(host) // the parent's tunnels, whichever alias is selected
	var b strings.Builder
	b.WriteString("\n\n" + m.styles.SectionLabel.Render("tunnels"))

	ts, known := m.tstatus[host.Alias]
	now := m.clock.Now()
	// The interactive control master is NOT a tunnel (Model B: forwards are
	// their own processes, they never ride it), so it is not a row here
	// (SASASH.4.25). A poll that could not run at all is still surfaced.
	if known && ts.err != nil {
		b.WriteString("\n   " + m.styles.Notice.Render("! status: "+ts.err.Error()))
		return b.String()
	}
	if ts.status.CorruptState {
		b.WriteString("\n   " + m.styles.Notice.Render("! state cache unreadable — treated as empty"))
	}
	if ts.status.CorruptStats {
		b.WriteString("\n   " + m.styles.Notice.Render("! stats cache unreadable — times start fresh from now"))
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
			// Observed run for up/down rows; a listening row's port is
			// accepting, so its "since" is the accepting run.
			if run := runWord(now, f.Since, f.DownSince); run != "" {
				switch {
				case f.State == tunnel.Up && strings.HasPrefix(run, "up"),
					f.State == tunnel.Down && strings.HasPrefix(run, "down"):
					word = run
				case f.State == tunnel.Listening && strings.HasPrefix(run, "up"):
					word = "listening " + strings.TrimPrefix(run, "up ")
				}
			}
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
		// While this row is being edited, the exact commands enter will
		// run sit under it and follow the typing: nothing silent. The
		// plan belongs to the row the edit is BOUND to — a poll can move
		// rows under the highlight, and the action would not follow.
		if m.mode == promptEditFwd && host.Alias == m.editFwd.host && f.Set == m.editFwd.set && f.Spec == m.editFwd.old {
			for _, line := range m.editFwdPlan(expandedOrTyped(m.prompt.Value())) {
				b.WriteString("\n       " + m.styles.CmdPreview.Render(line))
			}
		}
	}
	return b.String()
}

// lastSeenLine renders the host's remembered moments — the last
// interactive handoff and the last path switch — as "ago" times; each is
// omitted when unknown, and the line vanishes when both are.
func (m Model) lastSeenLine(st tunnel.HostStatus, now time.Time) string {
	var parts []string
	if ago := since(now, st.LastHandoff); ago != "" {
		parts = append(parts, "last connected "+ago+" ago")
	}
	if ago := since(now, st.LastSwitch); ago != "" {
		p := "last switch"
		if st.LastSwitchPath != "" {
			p += " → " + st.LastSwitchPath
		}
		parts = append(parts, p+" "+ago+" ago")
	}
	if len(parts) == 0 {
		return ""
	}
	return "\n   " + m.styles.PathExtra.Render(strings.Join(parts, " · "))
}
