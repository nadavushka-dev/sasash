package ui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/stats"
	"github.com/nadavushka-dev/sasash/internal/tunnel"
)

// sparkLen is how many recent probe results a path remembers this
// session — enough for a sparkline, small enough to stay per-frame cheap.
const sparkLen = 30

// pathHist is one path's session memory: the ring of recent probe
// results (a negative latency is a miss), how many rounds it answered,
// and when it was first seen down after being up — all session-observed,
// never persisted, so restarting the TUI honestly forgets them.
type pathHist struct {
	ring      []time.Duration // newest last; len ≤ sparkLen
	up, total int
	downSince time.Time // zero = not observed down after an up this session
	wasUp     bool
}

// observe folds one probe result into the history at now.
func (h *pathHist) observe(s probe.Status, now time.Time) {
	lat := time.Duration(-1)
	if s.Alive() {
		lat = s.Latency
		h.up++
		h.wasUp = true
		h.downSince = time.Time{}
	} else if h.wasUp {
		h.wasUp = false
		h.downSince = now
	}
	h.total++
	h.ring = append(h.ring, lat)
	if len(h.ring) > sparkLen {
		h.ring = h.ring[len(h.ring)-sparkLen:]
	}
}

// pathKey identifies a path in the session memory by what it IS — host,
// name, dial address — never by its position: a reload can reorder or
// drop paths, and history must follow the path, not the slot.
func pathKey(alias string, p probe.Path) string {
	return alias + "\x00" + p.Name + "\x00" + p.Addr()
}

// observeProbes folds one probe round into the session memory. Statuses
// are index-aligned with each host's Paths, which is where the identity
// comes from.
func (m Model) observeProbes(statuses map[string][]probe.Status) Model {
	now := m.clock.Now()
	for _, host := range m.hosts {
		sts := statuses[host.Alias]
		for i, p := range host.Paths {
			if i >= len(sts) {
				break
			}
			k := pathKey(host.Alias, p)
			h := m.hist[k]
			if h == nil {
				h = &pathHist{}
				m.hist[k] = h
			}
			h.observe(sts[i], now)
		}
	}
	return m
}

// pruneHist forgets paths that no longer exist after a reload, so the
// memory is bounded by the live config and a removed path's history can
// never be shown under another.
func (m Model) pruneHist() Model {
	live := make(map[string]bool)
	for _, host := range m.hosts {
		for _, p := range host.Paths {
			live[pathKey(host.Alias, p)] = true
		}
	}
	for k := range m.hist {
		if !live[k] {
			delete(m.hist, k)
		}
	}
	return m
}

// sparkGlyphs are the eight levels a latency maps onto; a miss is "·".
const sparkGlyphs = "▁▂▃▄▅▆▇█"

// sparkline draws the ring: each value scaled between the ring's min and
// max latency (a single value or a flat ring sits on the lowest bar), a
// miss as "·". Shape, not colour — it reads the same under NO_COLOR.
func sparkline(ring []time.Duration) string {
	var lo, hi time.Duration = -1, -1
	for _, d := range ring {
		if d < 0 {
			continue
		}
		if lo < 0 || d < lo {
			lo = d
		}
		if d > hi {
			hi = d
		}
	}
	glyphs := []rune(sparkGlyphs)
	var b strings.Builder
	for _, d := range ring {
		if d < 0 {
			b.WriteString("·")
			continue
		}
		level := 0
		if hi > lo {
			level = int(float64(d-lo) / float64(hi-lo) * float64(len(glyphs)-1))
		}
		b.WriteRune(glyphs[level])
	}
	return b.String()
}

// spellLatency is the range's spelling: milliseconds, or the finer unit
// when a round trip is under one — never "0s" for a path that answered.
func spellLatency(d time.Duration) string {
	if d < time.Millisecond {
		return d.Round(time.Microsecond).String()
	}
	return d.Round(time.Millisecond).String()
}

// latencyRange spells the ring's min–max, "" when nothing answered.
func latencyRange(ring []time.Duration) string {
	var lo, hi time.Duration = -1, -1
	for _, d := range ring {
		if d < 0 {
			continue
		}
		if lo < 0 || d < lo {
			lo = d
		}
		if d > hi {
			hi = d
		}
	}
	if lo < 0 {
		return ""
	}
	if lo == hi {
		return spellLatency(lo)
	}
	return spellLatency(lo) + "–" + spellLatency(hi)
}

// newest keeps the last n entries of the ring.
func newest(ring []time.Duration, n int) []time.Duration {
	if n < 0 {
		n = 0
	}
	if len(ring) > n {
		return ring[len(ring)-n:]
	}
	return ring
}

// since spells how long ago t was as of now: 42s, 12m, 1h03m, 2d04h.
// The zero time is unknown and spells "" — never a made-up zero.
func since(now, t time.Time) string {
	if t.IsZero() || now.Before(t) {
		return ""
	}
	d := now.Sub(t)
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
	return fmt.Sprintf("%dd%02dh", int(d.Hours())/24, int(d.Hours())%24)
}

// runWord renders an entity's observed run: "up 12m" / "down 3m" from
// the store's transition times, or "" when nothing was observed.
func runWord(now, upSince, downSince time.Time) string {
	if s := since(now, upSince); s != "" {
		return "up " + s
	}
	if s := since(now, downSince); s != "" {
		return "down " + s
	}
	return ""
}

// stampHandoff arranges the RecordHandoff stamp for a managed host after
// enter/H came back. The store wants one writer per host, and a status
// poll (which observes and writes) may be in flight: the stamp rides the
// next poll's goroutine — issued now when none is running, or deferred
// until the running one lands — so the two never race. Nothing to stamp
// when no hook is wired or the handoff had no managed host.
func (m Model) stampHandoff(host string) (Model, tea.Cmd) {
	if m.recordHandoff == nil || host == "" {
		return m, nil
	}
	rec := m.recordHandoff
	return m.stampVia(host, func() error { return rec(host) })
}

// stampVia queues one stamp for host and issues it with the next poll —
// now, or when the in-flight one lands. Stamps of different kinds
// (a handoff, then a switch) compose: both run, in order, on that poll.
func (m Model) stampVia(host string, stamp func() error) (Model, tea.Cmd) {
	if prev := m.pendingStamp[host]; prev != nil {
		next := stamp
		stamp = func() error { return errors.Join(prev(), next()) }
	}
	m.pendingStamp[host] = stamp
	if m.tinflight[host] || m.switching[host] {
		return m, nil // the landing poll (or the switch's done arm) re-polls with the stamp
	}
	return m.stampedPollCmd(host)
}

// stampedPollCmd issues the deferred stamp followed by a fresh poll on
// one goroutine, so the stamp is on disk before the render that shows it.
func (m Model) stampedPollCmd(host string) (Model, tea.Cmd) {
	stamp := m.pendingStamp[host]
	if stamp == nil {
		return m, nil
	}
	h, ok := m.hostByAlias(host)
	if !ok || m.tunnels == nil {
		delete(m.pendingStamp, host)
		return m, nil
	}
	delete(m.pendingStamp, host)
	// The handoff stamp is the connected alias's, but tunnel status is the
	// machine's: a handoff to a path-variant refreshes the parent, whose pane
	// the render shows (SASASH.4.24). tunnelHost is identity for a real host.
	th := m.tunnelHost(h)
	m.tinflight[th.Alias] = true
	eng := m.tunnels
	return m, func() tea.Msg {
		stampErr := stamp()
		ctx, cancel := context.WithTimeout(context.Background(), tunnelStatusTimeout)
		defer cancel()
		st, err := eng.Status(ctx, th.Alias, th.Forwards)
		return tunnelStatusMsg{host: th.Alias, status: st, err: err, stampErr: stampErr}
	}
}

// summaryLine is the header's tally: hosts, hosts with a path alive, and
// forwards up across the hosts polled so far. Before the first probe
// round the tally is unknown and says so.
func (m Model) summaryLine() string {
	// Folded variants are not hosts the user counts; the tally follows
	// the list.
	hosts := m.listedHosts()
	n := len(hosts)
	if n == 0 {
		return ""
	}
	if m.statuses == nil {
		return m.tally(hosts) + " · probing…"
	}
	reachable := 0
	for _, h := range hosts {
		if _, ok := probe.Select(m.statuses[h.Alias]); ok {
			reachable++
		}
	}
	line := fmt.Sprintf("%s · %d reachable", m.tally(hosts), reachable)
	if m.tunnels == nil {
		return line
	}
	up, polled := 0, 0
	for _, h := range hosts {
		ts, ok := m.tstatus[h.Alias]
		if !ok || ts.err != nil {
			continue
		}
		polled++
		for _, f := range ts.status.Forwards {
			if f.State == tunnel.Up {
				up++
			}
		}
	}
	if polled == 0 {
		return line
	}
	suffix := ""
	if polled < n {
		suffix = fmt.Sprintf(" (%d polled)", polled)
	}
	return line + fmt.Sprintf(" · %d %s up%s", up, plural(up, "tunnel"), suffix)
}

func plural(n int, word string) string {
	if n == 1 {
		return word
	}
	return word + "s"
}

// clockOr returns c, or wall time when none was injected.
func clockOr(c stats.Clock) stats.Clock {
	if c == nil {
		return time.Now
	}
	return c
}

// handoffReturned is the common tail of enter/H coming back: re-probe,
// and remember the handoff — only when one happened.
func (m Model) handoffReturned(started bool) (tea.Model, tea.Cmd) {
	host := m.handoffHost
	m.handoffHost = ""
	if !started {
		return m, probeCmd(m.prober, m.hosts)
	}
	m, stamp := m.stampHandoff(host)
	return m, tea.Batch(probeCmd(m.prober, m.hosts), stamp)
}

// pathList is every path's identity in config order — order included,
// because the probe round is index-aligned and a reorder is exactly what
// makes it lie.
func pathList(hosts []hostmodel.Host) []string {
	var out []string
	for _, h := range hosts {
		for _, p := range h.Paths {
			out = append(out, pathKey(h.Alias, p))
		}
	}
	return out
}

func samePaths(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
