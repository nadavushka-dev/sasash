package ui

import (
	"fmt"
	"net"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
)

// AdhocEntry is what the SaveHost hook persists after an ad-hoc connect:
// exactly the pieces the user typed, ready to become a Host block.
type AdhocEntry struct {
	Alias    string
	User     string
	Host     string
	Port     string
	Forwards []string // -L specs, verbatim
}

// adhocTarget is a parsed "[user@]host[:port] [-L spec]..." input.
type adhocTarget struct {
	User, Host, Port string
	Forwards         []string
}

// parseAdhoc parses the connect prompt. Malformed input returns an error
// for the notice line and never reaches exec (pin: "never a broken exec").
func parseAdhoc(input string) (adhocTarget, error) {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return adhocTarget{}, fmt.Errorf("type [user@]host[:port], optionally -L local:host:port")
	}

	var t adhocTarget
	dest := fields[0]
	if user, rest, ok := strings.Cut(dest, "@"); ok {
		if user == "" || rest == "" {
			return adhocTarget{}, fmt.Errorf("bad target %q: want [user@]host[:port]", fields[0])
		}
		t.User, dest = user, rest
	}
	host, port, err := splitDest(dest)
	if err != nil {
		return adhocTarget{}, err
	}
	t.Host, t.Port = host, port

	for i := 1; i < len(fields); i++ {
		spec := fields[i]
		switch {
		case spec == "-L":
			i++
			if i == len(fields) {
				return adhocTarget{}, fmt.Errorf("-L needs a forward spec")
			}
			spec = fields[i]
		case strings.HasPrefix(spec, "-L"):
			spec = spec[2:]
		default:
			return adhocTarget{}, fmt.Errorf("unexpected %q: only -L forwards are accepted", spec)
		}
		if strings.Count(spec, ":") < 2 {
			return adhocTarget{}, fmt.Errorf("forward %q: want local:host:port", spec)
		}
		t.Forwards = append(t.Forwards, spec)
	}
	return t, nil
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

// adhocExitMsg arrives when the terminal returns from an ad-hoc handoff.
type adhocExitMsg struct{ err error }

// adhocSavedMsg reports the SaveHost outcome.
type adhocSavedMsg struct {
	alias string
	err   error
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
		target, err := parseAdhoc(m.prompt.Value())
		if err != nil {
			m.notice = err.Error() // stay in the prompt for correction
			return m, nil
		}
		m.pending = target
		m.mode = promptNone
		m.prompt.Blur()
		argv := adhocArgv(target)
		return m, tea.ExecProcess(execCommand(argv), func(err error) tea.Msg {
			return adhocExitMsg{err: err}
		})

	case promptAlias:
		alias := strings.TrimSpace(m.prompt.Value())
		if alias == "" {
			m.mode = promptNone
			m.saveThenConnect = false
			m.prompt.Blur()
			m.notice = "not saved"
			return m, nil
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
		save := m.saveHost
		return m, func() tea.Msg {
			return adhocSavedMsg{alias: alias, err: save(entry)}
		}
	}
	return m, nil
}
