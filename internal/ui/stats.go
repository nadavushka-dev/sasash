package ui

import (
	"fmt"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"

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

// pathKey identifies a path in the session memory.
func pathKey(alias string, i int) string { return fmt.Sprintf("%s\x00%d", alias, i) }

// observeProbes folds one probe round into the session memory.
func (m Model) observeProbes(statuses map[string][]probe.Status) Model {
	now := m.clock.Now()
	for alias, sts := range statuses {
		for i, s := range sts {
			k := pathKey(alias, i)
			h := m.hist[k]
			if h == nil {
				h = &pathHist{}
				m.hist[k] = h
			}
			h.observe(s, now)
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
		return lo.Round(time.Millisecond).String()
	}
	return fmt.Sprintf("%v–%v", lo.Round(time.Millisecond), hi.Round(time.Millisecond))
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

// handoffRecordedMsg reports the RecordHandoff stamp after enter/H.
type handoffRecordedMsg struct {
	host string
	err  error
}

// recordHandoffCmd stamps the handoff through the hook, off the UI
// thread; nil when nothing is wired or the handoff had no managed host.
func (m Model) recordHandoffCmd(host string) tea.Cmd {
	if m.recordHandoff == nil || host == "" {
		return nil
	}
	rec := m.recordHandoff
	return func() tea.Msg { return handoffRecordedMsg{host: host, err: rec(host)} }
}

// summaryLine is the header's tally: hosts, hosts with a path alive, and
// forwards up across the hosts polled so far. Before the first probe
// round the tally is unknown and says so.
func (m Model) summaryLine() string {
	n := len(m.hosts)
	if n == 0 {
		return ""
	}
	if m.statuses == nil {
		return fmt.Sprintf("%d %s · probing…", n, plural(n, "host"))
	}
	reachable := 0
	for _, h := range m.hosts {
		if _, ok := probe.Select(m.statuses[h.Alias]); ok {
			reachable++
		}
	}
	line := fmt.Sprintf("%d %s · %d reachable", n, plural(n, "host"), reachable)
	if m.tunnels == nil {
		return line
	}
	up, polled := 0, 0
	for _, h := range m.hosts {
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
