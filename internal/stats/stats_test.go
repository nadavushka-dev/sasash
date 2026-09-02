package stats_test

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/stats"
)

var (
	t0 = time.Date(2026, 9, 2, 12, 0, 0, 0, time.UTC)
	t1 = t0.Add(1 * time.Minute)
	t2 = t0.Add(2 * time.Minute)
)

// tickStore returns a Store whose clock yields the given times in sequence,
// then keeps returning the last one.
func tickStore(t *testing.T, times ...time.Time) stats.Store {
	t.Helper()
	i := 0
	return stats.Store{Dir: t.TempDir(), Clock: func() time.Time {
		if i < len(times)-1 {
			i++
			return times[i-1]
		}
		return times[len(times)-1]
	}}
}

func TestClock_Now(t *testing.T) {
	t.Parallel()

	t.Run("nil clock is real time", func(t *testing.T) {
		t.Parallel()
		var c stats.Clock
		before := time.Now()
		got := c.Now()
		if got.Before(before) || time.Since(got) > time.Minute {
			t.Errorf("Now() = %v, want about the real now", got)
		}
	})

	t.Run("non-nil clock is itself", func(t *testing.T) {
		t.Parallel()
		c := stats.Clock(func() time.Time { return t0 })
		if got := c.Now(); !got.Equal(t0) {
			t.Errorf("Now() = %v, want %v", got, t0)
		}
	})
}

func TestStore_ObserveHost_Transitions(t *testing.T) {
	t.Parallel()
	type step struct {
		master   bool
		forwards map[string]bool
	}
	tests := []struct {
		name  string
		steps []step
		want  stats.Host
	}{
		{
			name:  "never seen stays unknown",
			steps: []step{{master: false}},
			want:  stats.Host{},
		},
		{
			name:  "alive starts a run",
			steps: []step{{master: true}},
			want:  stats.Host{MasterSince: t0},
		},
		{
			name:  "alive again keeps the original since",
			steps: []step{{master: true}, {master: true}},
			want:  stats.Host{MasterSince: t0},
		},
		{
			name:  "dead after alive stamps down-since",
			steps: []step{{master: true}, {master: false}},
			want:  stats.Host{MasterDownSince: t1},
		},
		{
			name:  "alive after down starts a fresh run",
			steps: []step{{master: true}, {master: false}, {master: true}},
			want:  stats.Host{MasterSince: t2},
		},
		{
			name: "forward machine runs per spec",
			steps: []step{
				{master: true, forwards: map[string]bool{"3000:localhost:3000": true, "4011:localhost:4011": false}},
				{master: true, forwards: map[string]bool{"3000:localhost:3000": false, "4011:localhost:4011": true}},
			},
			want: stats.Host{
				MasterSince: t0,
				Forwards: map[string]stats.ForwardObs{
					"3000:localhost:3000": {DownSince: t1},
					"4011:localhost:4011": {Since: t1},
				},
			},
		},
		{
			name: "absent specs are pruned",
			steps: []step{
				{master: true, forwards: map[string]bool{"3000:localhost:3000": true}},
				{master: true, forwards: map[string]bool{"4011:localhost:4011": true}},
			},
			want: stats.Host{
				MasterSince: t0,
				Forwards:    map[string]stats.ForwardObs{"4011:localhost:4011": {Since: t1}},
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			s := tickStore(t, t0, t1, t2)
			var got stats.Host
			for _, st := range tt.steps {
				var err error
				if got, _, err = s.ObserveHost("ubuntu", st.master, st.forwards); err != nil {
					t.Fatalf("ObserveHost() err = %v", err)
				}
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("ObserveHost() = %+v, want %+v", got, tt.want)
			}
			// And the persisted view must agree with the returned one.
			loaded, corrupt := s.Load("ubuntu")
			if corrupt || !reflect.DeepEqual(loaded, tt.want) {
				t.Errorf("Load() = %+v (corrupt %v), want %+v", loaded, corrupt, tt.want)
			}
		})
	}
}

func TestStore_RecordMasterStart(t *testing.T) {
	t.Parallel()
	s := tickStore(t, t0, t1, t2)
	if err := s.RecordMasterStart("ubuntu"); err != nil {
		t.Fatalf("RecordMasterStart() err = %v", err)
	}
	got, _ := s.Load("ubuntu")
	if !got.MasterBySasash || !got.MasterSince.Equal(t0) || !got.MasterDownSince.IsZero() {
		t.Errorf("Load() = %+v, want sasash-started run at t0", got)
	}

	// A later alive observation keeps the sasash-started run; a dead one
	// ends it and clears the origin.
	if _, _, err := s.ObserveHost("ubuntu", true, nil); err != nil {
		t.Fatalf("ObserveHost(alive) err = %v", err)
	}
	got, _ = s.Load("ubuntu")
	if !got.MasterBySasash || !got.MasterSince.Equal(t0) {
		t.Errorf("after alive observation = %+v, want run kept", got)
	}
	if _, _, err := s.ObserveHost("ubuntu", false, nil); err != nil {
		t.Fatalf("ObserveHost(dead) err = %v", err)
	}
	got, _ = s.Load("ubuntu")
	if got.MasterBySasash || !got.MasterSince.IsZero() || !got.MasterDownSince.Equal(t2) {
		t.Errorf("after dead observation = %+v, want down at t2 with origin cleared", got)
	}
}

// The common real-world sequence: sasash starts a master that was observed
// down. The down marker must clear, or the illegal both-set shape appears.
func TestStore_RecordMasterStart_AfterDownRun(t *testing.T) {
	t.Parallel()
	s := tickStore(t, t0, t1, t2)
	if _, _, err := s.ObserveHost("ubuntu", true, nil); err != nil {
		t.Fatalf("ObserveHost(alive) err = %v", err)
	}
	if _, _, err := s.ObserveHost("ubuntu", false, nil); err != nil {
		t.Fatalf("ObserveHost(dead) err = %v", err)
	}
	if err := s.RecordMasterStart("ubuntu"); err != nil {
		t.Fatalf("RecordMasterStart() err = %v", err)
	}
	got, _ := s.Load("ubuntu")
	if !got.MasterSince.Equal(t2) || !got.MasterDownSince.IsZero() || !got.MasterBySasash {
		t.Errorf("Load() = %+v, want fresh sasash run at t2 with the down marker cleared", got)
	}
}

func TestStore_RecordHooks(t *testing.T) {
	t.Parallel()
	s := tickStore(t, t0, t1)
	if err := s.RecordHandoff("ubuntu"); err != nil {
		t.Fatalf("RecordHandoff() err = %v", err)
	}
	if err := s.RecordSwitch("ubuntu", "lan"); err != nil {
		t.Fatalf("RecordSwitch() err = %v", err)
	}
	got, corrupt := s.Load("ubuntu")
	want := stats.Host{LastHandoff: t0, LastSwitch: t1, LastSwitchPath: "lan"}
	if corrupt || !reflect.DeepEqual(got, want) {
		t.Errorf("Load() = %+v (corrupt %v), want %+v", got, corrupt, want)
	}
}

func TestStore_Load(t *testing.T) {
	t.Parallel()

	t.Run("missing is unknown not corrupt", func(t *testing.T) {
		t.Parallel()
		s := stats.Store{Dir: t.TempDir()}
		got, corrupt := s.Load("ubuntu")
		if corrupt || !reflect.DeepEqual(got, stats.Host{}) {
			t.Errorf("Load() = %+v (corrupt %v), want zero, false", got, corrupt)
		}
	})

	t.Run("corrupt is unknown plus flag", func(t *testing.T) {
		t.Parallel()
		s := stats.Store{Dir: t.TempDir()}
		if err := os.WriteFile(filepath.Join(s.Dir, "ubuntu.stats.json"), []byte("{nope"), 0o600); err != nil {
			t.Fatalf("WriteFile() err = %v", err)
		}
		got, corrupt := s.Load("ubuntu")
		if !corrupt || !reflect.DeepEqual(got, stats.Host{}) {
			t.Errorf("Load() = %+v (corrupt %v), want zero, true", got, corrupt)
		}
	})

	t.Run("unreadable is unknown plus flag", func(t *testing.T) {
		t.Parallel()
		if os.Geteuid() == 0 {
			t.Skip("chmod 000 does not stop root")
		}
		s := stats.Store{Dir: t.TempDir()}
		if err := os.WriteFile(filepath.Join(s.Dir, "ubuntu.stats.json"), []byte("{}"), 0o000); err != nil {
			t.Fatalf("WriteFile() err = %v", err)
		}
		if got, corrupt := s.Load("ubuntu"); !corrupt || !reflect.DeepEqual(got, stats.Host{}) {
			t.Errorf("Load() = %+v (corrupt %v), want zero, true", got, corrupt)
		}
	})

	t.Run("invalid alias is unknown not corrupt", func(t *testing.T) {
		t.Parallel()
		s := stats.Store{Dir: t.TempDir()}
		if got, corrupt := s.Load("../evil"); corrupt || !reflect.DeepEqual(got, stats.Host{}) {
			t.Errorf("Load() = %+v (corrupt %v), want zero, false", got, corrupt)
		}
	})
}

func TestStore_HostileAliasNeverEscapes(t *testing.T) {
	t.Parallel()
	s := stats.Store{Dir: t.TempDir(), Clock: func() time.Time { return t0 }}
	if err := s.RecordHandoff("../evil"); err == nil {
		t.Error("RecordHandoff(../evil) err = nil, want error")
	}
	if _, statErr := os.Lstat(filepath.Join(s.Dir, "..", "evil.stats.json")); statErr == nil {
		t.Error("stats file escaped Dir")
	}
}

func TestStore_ObserveHost_WriteFailureStillReturnsView(t *testing.T) {
	t.Parallel()
	s := stats.Store{Dir: t.TempDir(), Clock: func() time.Time { return t0 }}
	// Block the file slot with a directory: the write must fail, the
	// returned view must still be correct for this render.
	if err := os.Mkdir(filepath.Join(s.Dir, "ubuntu.stats.json"), 0o700); err != nil {
		t.Fatalf("Mkdir() err = %v", err)
	}
	got, corrupt, err := s.ObserveHost("ubuntu", true, nil)
	if err == nil {
		t.Error("ObserveHost() err = nil, want write failure")
	}
	if !got.MasterSince.Equal(t0) {
		t.Errorf("ObserveHost() = %+v, want the view stamped at t0 regardless", got)
	}
	// The blocked file reads as corrupt: base unknown, not invented.
	if !corrupt {
		t.Error("ObserveHost() corrupt = false, want true for an unreadable slot")
	}
}

func TestStore_PersistsAcrossInstances(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	first := stats.Store{Dir: dir, Clock: func() time.Time { return t0 }}
	if _, _, err := first.ObserveHost("ubuntu", true, map[string]bool{"3000:localhost:3000": true}); err != nil {
		t.Fatalf("ObserveHost() err = %v", err)
	}
	second := stats.Store{Dir: dir, Clock: func() time.Time { return t1 }}
	got, corrupt := second.Load("ubuntu")
	want := stats.Host{
		MasterSince: t0,
		Forwards:    map[string]stats.ForwardObs{"3000:localhost:3000": {Since: t0}},
	}
	if corrupt || !reflect.DeepEqual(got, want) {
		t.Errorf("Load() = %+v (corrupt %v), want %+v", got, corrupt, want)
	}
}
