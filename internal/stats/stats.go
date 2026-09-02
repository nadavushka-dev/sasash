// Package stats is the observation store and the project's Clock seam
// (SPEC §12, SASASH.4.7). It remembers WHEN reality was observed — master
// and forward up/down transitions, the last interactive handoff, the last
// path switch — in <host>.stats.json files under Dir.
//
// Every timestamp is an observation stamped by Clock at the moment something
// was seen; the zero time means unknown and is never invented, defaulted, or
// backdated. The files follow the same rules as the tunnel's forwards cache:
// a cache, never truth — missing means unknown, corrupt degrades to unknown
// with a flag, and no host alias can escape Dir.
// Contract: .squad/CONTRACT-stats.md (ratified).
package stats

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"time"
)

// Clock is the injectable time source SPEC §12 sketched. The nil Clock is
// the real one.
type Clock func() time.Time

// Now returns the clock's time; a nil Clock reads time.Now.
func (c Clock) Now() time.Time {
	if c == nil {
		return time.Now()
	}
	return c()
}

// Store reads and writes per-host observation files under Dir. Callers
// serialize per host, as with the tunnel cache.
type Store struct {
	Dir   string
	Clock Clock
}

// Host is one host's observations. The zero time means unknown.
type Host struct {
	// MasterSince is the start of the current observed-alive run.
	MasterSince time.Time `json:"masterSince,omitzero"`
	// MasterDownSince is the first observation of the master dead after
	// having been alive.
	MasterDownSince time.Time `json:"masterDownSince,omitzero"`
	// MasterBySasash marks the current alive-run as sasash-started.
	MasterBySasash bool `json:"masterBySasash,omitzero"`
	// LastHandoff is the last interactive handoff (enter/H), recorded by
	// the cmd/ui layer through RecordHandoff.
	LastHandoff time.Time `json:"lastHandoff,omitzero"`
	// LastSwitch and LastSwitchPath record the last path switch, stamped
	// by RecordSwitch at the moment the switch was observed.
	LastSwitch     time.Time `json:"lastSwitch,omitzero"`
	LastSwitchPath string    `json:"lastSwitchPath,omitzero"`
	// Forwards holds per-forward observations, keyed by verbatim spec.
	Forwards map[string]ForwardObs `json:"forwards,omitzero"`
}

// ForwardObs is one forward's observation pair.
type ForwardObs struct {
	Since     time.Time `json:"since,omitzero"`
	DownSince time.Time `json:"downSince,omitzero"`
}

// observe advances one {Since, DownSince} run machine. Three legal shapes:
// alive-run (Since set), down-after-alive (DownSince set), never-seen
// (both zero). onDown fires when an alive-run ends.
func observe(since, downSince *time.Time, alive bool, now time.Time, onDown func()) {
	switch {
	case alive && since.IsZero():
		*since = now
		*downSince = time.Time{}
	case !alive && !since.IsZero():
		*downSince = now
		*since = time.Time{}
		if onDown != nil {
			onDown()
		}
	}
}

// ObserveHost applies one render's reality — the master's aliveness and each
// forward's local-port acceptance — and persists the transitions. The
// returned Host is the post-transition view and is correct for this render
// even when the write fails; err then reports the failure and the caller may
// ignore it (observations go stale, reality still wins everywhere). corrupt
// reports that the prior file was unreadable or unparseable, so the base was
// unknown rather than invented. Forward observations for specs absent from
// forwards are pruned — an unattributable observation is a stale record.
func (s Store) ObserveHost(host string, master bool, forwards map[string]bool) (Host, bool, error) {
	h, corrupt := s.Load(host)
	now := s.Clock.Now()

	observe(&h.MasterSince, &h.MasterDownSince, master, now, func() {
		// The next master's origin is unknown.
		h.MasterBySasash = false
	})

	kept := make(map[string]ForwardObs, len(forwards))
	for spec, accepting := range forwards {
		obs := h.Forwards[spec]
		observe(&obs.Since, &obs.DownSince, accepting, now, nil)
		kept[spec] = obs
	}
	if len(kept) == 0 {
		kept = nil
	}
	h.Forwards = kept

	return h, corrupt, s.save(host, h)
}

// RecordMasterStart marks the about-to-be alive run as sasash-started.
func (s Store) RecordMasterStart(host string) error {
	return s.mutate(host, func(h *Host) {
		h.MasterBySasash = true
		h.MasterSince = s.Clock.Now()
		h.MasterDownSince = time.Time{}
	})
}

// RecordHandoff stamps the last interactive handoff.
func (s Store) RecordHandoff(host string) error {
	return s.mutate(host, func(h *Host) { h.LastHandoff = s.Clock.Now() })
}

// RecordSwitch stamps the last path switch and the path it switched to.
func (s Store) RecordSwitch(host, pathName string) error {
	return s.mutate(host, func(h *Host) {
		h.LastSwitch = s.Clock.Now()
		h.LastSwitchPath = pathName
	})
}

// Load reads without observing. Missing means a zero Host and false;
// corrupt or unreadable means a zero Host and true. Never an error, never
// an invented time.
func (s Store) Load(host string) (Host, bool) {
	path, err := s.path(host)
	if err != nil {
		return Host{}, false
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return Host{}, !errors.Is(err, fs.ErrNotExist)
	}
	var h Host
	if json.Unmarshal(data, &h) != nil {
		return Host{}, true
	}
	return h, false
}

func (s Store) mutate(host string, apply func(*Host)) error {
	h, _ := s.Load(host)
	apply(&h)
	return s.save(host, h)
}

func (s Store) save(host string, h Host) error {
	path, err := s.path(host)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o700); err != nil {
		return err
	}
	data, err := json.Marshal(h)
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o600)
}

// path refuses a host alias that is not a plain file name — an alias like
// "../x" must not walk out of Dir.
func (s Store) path(host string) (string, error) {
	if host == "" || host == "." || host == ".." || host != filepath.Base(host) {
		return "", fmt.Errorf("host %q is not a valid stats file name", host)
	}
	return filepath.Join(s.Dir, host+".stats.json"), nil
}
