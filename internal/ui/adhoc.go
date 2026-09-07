package ui

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strings"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/run"
)

// AdhocEntry is what the SaveHost hook persists after an ad-hoc connect:
// exactly the pieces the user typed, ready to become a Host block.
type AdhocEntry struct {
	Alias    string
	User     string
	Host     string
	Port     string
	Forwards []string // -L specs, canonical (shorthands expanded at parse time)
}

// adhocTarget is a parsed "[user@]host[:port] [-L spec]..." input.
// TunnelOnly is the -N token: no tty, no handoff — `ssh -fN` through the
// run seam in the background.
type adhocTarget struct {
	User, Host, Port string
	Forwards         []string
	TunnelOnly       bool
	// FromSelection marks a target the input did not name: the selected
	// host's alias stood in (SASASH.4.12). It is already saved, so no
	// save offer follows.
	FromSelection bool
}

// errNoTarget is the parse outcome the selection may complete.
var errNoTarget = errors.New("no target: want [user@]host[:port]")

// parseAdhocFor parses the connect prompt with the selected host's alias
// as the default target: an input that carries flags or specs but no
// [user@]host token connects to the selection — the alias, verbatim, so
// the config and the ControlMaster apply and a hand-written alias with
// ':' or '@' in it is never re-read as host:port or user@host. Every
// other outcome is parseAdhoc's: an explicit target is untouched, an
// empty input or an incomplete flag keeps its own error, and no
// selection keeps the no-target error.
func parseAdhocFor(input, selected string) (adhocTarget, error) {
	t, err := parseAdhoc(input)
	if !errors.Is(err, errNoTarget) || selected == "" {
		return t, err
	}
	t.Host, t.FromSelection = selected, true
	return t, nil
}

// parseAdhoc parses the connect prompt. Malformed input returns an error
// for the notice line and never reaches exec (pin: "never a broken exec").
func parseAdhoc(input string) (adhocTarget, error) {
	fields := strings.Fields(input)
	// A pasted command: the leading ssh token is noise, options may come
	// before the destination as ssh takes them.
	if len(fields) > 0 && fields[0] == "ssh" {
		fields = fields[1:]
	}
	if len(fields) == 0 {
		return adhocTarget{}, fmt.Errorf("type [user@]host[:port], optionally -L local:host:port, -N for a tunnel only")
	}

	var t adhocTarget
	dest, flagPort := "", ""
	for i := 0; i < len(fields); i++ {
		f := fields[i]
		switch {
		case f == "-N":
			t.TunnelOnly = true
		case f == "-L" || f == "-p":
			i++
			if i == len(fields) {
				return adhocTarget{}, fmt.Errorf("%s needs a value", f)
			}
			if f == "-L" {
				if err := addForward(&t, fields[i]); err != nil {
					return adhocTarget{}, err
				}
			} else {
				flagPort = fields[i]
			}
		case strings.HasPrefix(f, "-L"):
			if err := addForward(&t, f[2:]); err != nil {
				return adhocTarget{}, err
			}
		case strings.HasPrefix(f, "-p"):
			flagPort = f[2:]
		case strings.HasPrefix(f, "-"):
			return adhocTarget{}, fmt.Errorf("unexpected %q: only -L, -N and -p are accepted", f)
		case dest != "":
			return adhocTarget{}, fmt.Errorf("unexpected %q after the target %q", f, dest)
		default:
			dest = f
		}
	}
	if dest == "" {
		// Everything but the target parsed: hand it back with the sentinel
		// so a caller with a default target need not parse twice.
		if flagPort != "" && !numeric(flagPort) {
			return adhocTarget{}, fmt.Errorf("bad port %q", flagPort)
		}
		t.Port = flagPort
		return t, errNoTarget
	}
	target := dest
	if user, rest, ok := strings.Cut(dest, "@"); ok {
		if user == "" || rest == "" {
			return adhocTarget{}, fmt.Errorf("bad target %q: want [user@]host[:port]", target)
		}
		t.User, dest = user, rest
	}
	host, port, err := splitDest(dest)
	if err != nil {
		return adhocTarget{}, err
	}
	if flagPort != "" {
		if port != "" && port != flagPort {
			return adhocTarget{}, fmt.Errorf("port given twice: %q and -p %s", target, flagPort)
		}
		if !numeric(flagPort) {
			return adhocTarget{}, fmt.Errorf("bad port %q", flagPort)
		}
		port = flagPort
	}
	t.Host, t.Port = host, port
	return t, nil
}

// addForward accepts one -L spec, a shorthand expanded to its canonical
// three-part form so everything downstream sees only that; the prompt's
// own rule — a spec has at least two colons — still rejects the rest
// (3000:web is malformed, never 3000:localhost:web).
func addForward(t *adhocTarget, spec string) error {
	full, _ := expandSpec(spec)
	if strings.Count(full, ":") < 2 {
		return fmt.Errorf("forward %q: want local:host:port", spec)
	}
	t.Forwards = append(t.Forwards, full)
	return nil
}

// splitDest splits "host[:port]", mirroring hostmodel.splitAddr's rules
// (kept in sync by hand — hostmodel is another lane's package): a bare
// IPv6 literal (::1 or [::1]) is a host with no port; an empty host or a
// present-but-bad port is an error, never silently defaulted.
func splitDest(s string) (host, port string, err error) {
	// A leading dash would land as ssh's destination argument and be
	// parsed as an option — a guaranteed-broken exec, so reject it here.
	if strings.HasPrefix(s, "-") {
		return "", "", fmt.Errorf("bad target %q: a host cannot start with -", s)
	}
	if strings.HasPrefix(s, "[") && strings.HasSuffix(s, "]") {
		if h := s[1 : len(s)-1]; h != "" {
			return h, "", nil
		}
		return "", "", fmt.Errorf("empty host")
	}
	h, p, splitErr := net.SplitHostPort(s)
	if splitErr != nil {
		if s == "" {
			return "", "", fmt.Errorf("empty host")
		}
		return s, "", nil
	}
	if h == "" || p == "" || !numeric(p) {
		return "", "", fmt.Errorf("bad target %q: want host or host:port", s)
	}
	return h, p, nil
}

func numeric(s string) bool {
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return s != ""
}

// adhocArgv is the exact ssh invocation for a parsed target — the ad-hoc
// twin of sshCommand, built once so the handoff cannot drift from what
// the prompt promised.
func adhocArgv(t adhocTarget) []string {
	argv := []string{"ssh"}
	for _, f := range t.Forwards {
		argv = append(argv, "-L", f)
	}
	if t.Port != "" {
		argv = append(argv, "-p", t.Port)
	}
	dest := t.Host
	if t.User != "" {
		dest = t.User + "@" + t.Host
	}
	return append(argv, dest)
}

// adhocDest is the [user@]host token as ssh prints it back.
func adhocDest(t adhocTarget) string {
	if t.User != "" {
		return t.User + "@" + t.Host
	}
	return t.Host
}

// adhocTunnelArgv is the tunnel-only (-N) invocation: ssh backgrounds
// itself after auth (-f) and runs no command (-N), so the TUI keeps the
// terminal. BatchMode is what makes that honest: -f authenticates before
// it forks and would otherwise open /dev/tty for a passphrase — over the
// alt screen, racing the TUI for keys, until the timeout. With BatchMode
// ssh refuses to prompt and fails at once with its reason, which the
// notice shows with the hint. Same destination and forwards as adhocArgv.
func adhocTunnelArgv(t adhocTarget) []string {
	argv := []string{"ssh", "-fN", "-o", "BatchMode=yes"}
	return append(argv, adhocArgv(t)[1:]...)
}

// adhocTunnelDoneMsg reports the background `ssh -fN`: err is a seam
// failure (ssh never ran); res is ssh's own result.
type adhocTunnelDoneMsg struct {
	res run.Result
	err error
}

// adhocTunnelCmd runs the tunnel-only ad-hoc through the run seam. Auth
// must be keys or an agent (BatchMode): anything that would prompt fails
// — reported with the hint, never hung.
func adhocTunnelCmd(r run.Runner, t adhocTarget) tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), tunnelActionTimeout)
		defer cancel()
		res, err := r.Run(ctx, adhocTunnelArgv(t)...)
		return adhocTunnelDoneMsg{res: res, err: err}
	}
}

// adhocExitMsg arrives when the terminal returns from an ad-hoc handoff.
type adhocExitMsg struct{ err error }

// adhocSavedMsg reports the SaveHost outcome; tunnel and target say
// whether a -N waited on it and for what.
type adhocSavedMsg struct {
	alias  string
	err    error
	tunnel bool
	target adhocTarget
}

// aliasProblem validates a proposed alias against ssh_config token rules
// and the currently loaded hosts; "" means acceptable.
func (m Model) aliasProblem(alias string) string {
	// A leading dash would make `ssh <alias>` parse the alias as an
	// option forever after; a leading # would comment the Host line out.
	if strings.HasPrefix(alias, "-") || strings.HasPrefix(alias, "#") ||
		strings.ContainsAny(alias, " \t\"") || strings.ContainsAny(alias, "*?!") {
		return fmt.Sprintf("alias %q: want a single plain word", alias)
	}
	for _, h := range m.hosts {
		if h.Alias == alias {
			return fmt.Sprintf("alias %q already exists", alias)
		}
	}
	return ""
}

// submitNewConn is enter in the settings "add new connection" prompt:
// the target is parsed like an ad-hoc one, but saved FIRST — the alias
// prompt follows, and a successful save offers the connect.
func (m Model) submitNewConn() (Model, tea.Cmd) {
	target, err := parseAdhoc(m.prompt.Value())
	if err != nil {
		m.notice = err.Error() // stay in the prompt for correction
		return m, nil
	}
	if target.TunnelOnly {
		m.notice = "-N belongs to the a prompt — this saves a connection, it starts nothing"
		return m, nil
	}
	if m.saveHost == nil {
		m.mode = promptNone
		m.prompt.Blur()
		m.notice = "saving is not available"
		return m, nil
	}
	m.pending = target
	m.saveThenConnect = true
	m.mode = promptAlias
	m.prompt.Reset()
	m.prompt.Placeholder = "alias to save as — esc to cancel"
	return m, m.prompt.Focus()
}

// promptSubmit handles enter inside either prompt mode.
func (m Model) promptSubmit() (Model, tea.Cmd) {
	switch m.mode {
	case promptConnect:
		target, err := parseAdhocFor(m.prompt.Value(), m.selectedAlias())
		if err != nil {
			m.notice = err.Error() // stay in the prompt for correction
			return m, nil
		}
		if target.TunnelOnly {
			if why := m.tunnelRefusal(target); why != "" {
				m.notice = why // stay in the prompt for correction
				return m, nil
			}
		}
		m.pending = target
		m.mode = promptNone
		m.prompt.Blur()
		if target.TunnelOnly {
			return m.startTunnelOnly(target)
		}
		argv := adhocArgv(target)
		return m, tea.ExecProcess(execCommand(argv), func(err error) tea.Msg {
			return adhocExitMsg{err: err}
		})

	case promptAlias:
		alias := strings.TrimSpace(m.prompt.Value())
		if alias == "" {
			return m.declineSave()
		}
		if problem := m.aliasProblem(alias); problem != "" {
			m.notice = problem
			return m, nil
		}
		m.mode = promptNone
		m.prompt.Blur()
		entry := AdhocEntry{
			Alias:    alias,
			User:     m.pending.User,
			Host:     m.pending.Host,
			Port:     m.pending.Port,
			Forwards: m.pending.Forwards,
		}
		// The save runs off the thread while keys keep flowing: what it
		// was for travels with it, not in the one pending slot (4.17).
		save, tunnel, target := m.saveHost, m.tunnelFirst, m.pending
		m.tunnelFirst = false
		return m, func() tea.Msg {
			return adhocSavedMsg{alias: alias, err: save(entry), tunnel: tunnel, target: target}
		}
	}
	return m, nil
}

// declineSave is esc, or an empty alias, in the save prompt: nothing is
// written. A tunnel waiting on the save (SASASH.4.17) is not started
// behind the user's back — the choice to run it unmanaged is put up
// front, in a popup that says what that means.
func (m Model) declineSave() (Model, tea.Cmd) {
	m.mode = promptNone
	m.saveThenConnect = false
	m.prompt.Blur()
	if !m.tunnelFirst {
		m.notice = "not saved"
		return m, nil
	}
	m.tunnelFirst = false
	return m.offerUnmanagedTunnel(), nil
}

// Tunnel-only ad-hoc (-N), SASASH.4.17. The 4.6 path ran `ssh -fN`
// raw through the run seam: no ControlMaster, no state record, so the
// tunnel was up but invisible to Status and, for a bare user@host, not
// x/X's to stop — against SPEC §4's honesty rule. Now a target that is a
// saved host goes through the engine like `+` (recorded, shown,
// stoppable); an unsaved one is saved first, then goes the same way
// through its new alias. Declining the save is honest up front: the
// unmanaged run is offered, never silent.

// savedTunnelTarget is the alias a -N target names when it IS a saved
// host: the bare alias, as the selection supplies it or as typed.
func (m Model) savedTunnelTarget(t adhocTarget) (string, bool) {
	if t.User != "" || t.Port != "" {
		return "", false
	}
	if _, ok := m.hostByAlias(t.Host); ok {
		return t.Host, true
	}
	return "", false
}

// tunnelRefusal is why a -N cannot run at all: a saved alias with a
// user or a port beside it. It is neither the alias (whose block sets
// those) nor a new destination (a save would write `HostName <alias>`,
// which ssh resolves as a name — a broken block); "" when it can.
func (m Model) tunnelRefusal(t adhocTarget) string {
	if t.User == "" && t.Port == "" {
		return ""
	}
	if _, ok := m.hostByAlias(t.Host); !ok {
		return ""
	}
	return t.Host + " is saved: -N " + t.Host + " alone, or its address"
}

// startTunnelOnly is enter on a -N input: the engine for a saved host,
// the save prompt first for anything else.
func (m Model) startTunnelOnly(t adhocTarget) (Model, tea.Cmd) {
	if alias, ok := m.savedTunnelTarget(t); ok {
		return m.engineTunnel(alias, t.Forwards)
	}
	if m.saveHost == nil {
		// Nowhere to save: the unmanaged run is the only run, and it is
		// still a choice.
		return m.offerUnmanagedTunnel(), nil
	}
	m.tunnelFirst = true
	m.mode = promptAlias
	m.prompt.Reset()
	m.prompt.Placeholder = "alias — saved, the tunnel is shown and stoppable · esc: run it unmanaged?"
	return m, m.prompt.Focus()
}

// engineTunnel brings the ad-hoc forwards up on a saved host through the
// engine, under the host's in-flight gate, exactly as `+` does: the
// master first if it is down, then each spec, recorded as the ad-hoc
// set so the tunnels pane shows it and x/X can end it.
func (m Model) engineTunnel(alias string, specs []string) (Model, tea.Cmd) {
	if m.tunnels == nil {
		m.notice = "tunnels are not available here — nothing started"
		return m, nil
	}
	// One spec is `+` exactly: the local port is dialled first, and a
	// held port comes back as the remap offer, never a silent failure.
	if len(specs) == 1 {
		if port, _ := localPort(specs[0]); port != "" {
			m.notice = "starting " + strings.Join(m.engineTunnelPreview(alias, specs), " ; ") + " …"
			return m, m.addForwardCmd(alias, specs[0], port)
		}
	}
	eng := m.tunnels
	done := "master up on " + alias
	if len(specs) > 0 {
		done = "tunnel up on " + alias + ": " + strings.Join(specs, " ") + " — ad-hoc, in the tunnels pane"
	}
	m.notice = "starting " + strings.Join(m.engineTunnelPreview(alias, specs), " ; ") + " …"
	return m, m.tunnelActionCmd(alias, "tunnel", done, func(ctx context.Context) error {
		return eng.Up(ctx, alias, "", specs)
	})
}

// engineTunnelPreview is the engine's flow for a -N as the prompt's
// expansion line shows it: the master start when the last poll saw it
// down (the pane's own gate), then each forward — the engine's own
// argv, pinned to its calls like the tunnels pane's preview (the check
// itself is not a step to show). Empty when nothing would run.
func (m Model) engineTunnelPreview(alias string, specs []string) []string {
	var steps []string
	if ts, ok := m.tstatus[alias]; !ok || !ts.status.Master {
		steps = append(steps, strings.Join(startArgv(alias), " "))
	}
	for _, s := range specs {
		steps = append(steps, strings.Join(forwardArgv(alias, s), " "))
	}
	return steps
}

// unmanagedCleanup is how a raw `ssh -fN` is found and ended: it has no
// ControlMaster socket for -O exit to reach.
const unmanagedCleanup = "to stop it later: pgrep -fl 'ssh -fN' and kill that pid"

// offerUnmanagedTunnel is the honest choice for a -N with no saved host
// behind it: a confirm popup — enter runs the raw `ssh -fN` anyway, esc
// runs nothing — that says what unmanaged means before anything starts.
func (m Model) offerUnmanagedTunnel() Model {
	argv := strings.Join(adhocTunnelArgv(m.pending), " ")
	m.notice = "unmanaged tunnel: enter starts it anyway, esc cancels"
	m = m.show("run an unmanaged tunnel? enter starts it · esc cancels",
		argv,
		"not saved, so its ssh has no sasash.conf block and no ControlMaster from it: it will not appear in the tunnels pane and x/X cannot stop it",
		unmanagedCleanup)
	m.popup.confirm, m.popup.verb = Model.runUnmanagedTunnel, "run it"
	return m
}

// runUnmanagedTunnel is enter in that popup: the raw path, kept for this
// one case.
func (m Model) runUnmanagedTunnel() (Model, tea.Cmd) {
	m.notice = "starting unmanaged " + strings.Join(adhocTunnelArgv(m.pending), " ") + " …"
	return m, adhocTunnelCmd(m.runner, m.pending)
}

// selectedAlias is the connect prompt's default target: the selected
// host's alias, "" with nothing selected.
func (m Model) selectedAlias() string {
	if host, ok := m.Selected(); ok {
		return host.Alias
	}
	return ""
}

// offerSave follows an ad-hoc connect: the alias prompt takes the line
// when saving is wired and the target was typed; a target that came from
// the selection is already saved. Either way the paths re-probe.
func (m Model) offerSave() (tea.Model, tea.Cmd) {
	if m.saveHost == nil || m.pending.FromSelection {
		return m, probeCmd(m.prober, m.hosts)
	}
	m.mode = promptAlias
	m.prompt.Reset()
	m.prompt.Placeholder = "alias to save as — esc to skip"
	return m, tea.Batch(m.prompt.Focus(), probeCmd(m.prober, m.hosts))
}
