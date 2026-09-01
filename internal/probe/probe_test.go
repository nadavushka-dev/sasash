package probe_test

import (
	"context"
	"errors"
	"net"
	"reflect"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
)

func TestPath_Addr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		path probe.Path
		want string
	}{
		{"default port", probe.Path{Host: "192.168.3.141"}, "192.168.3.141:22"},
		{"explicit port", probe.Path{Host: "203.0.113.150", Port: "22022"}, "203.0.113.150:22022"},
		{"hostname", probe.Path{Host: "ubuntu.local"}, "ubuntu.local:22"},
		{"ipv6 is bracketed", probe.Path{Host: "fd7a::1"}, "[fd7a::1]:22"},
		{"ipv6 with port", probe.Path{Host: "::1", Port: "2222"}, "[::1]:2222"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.path.Addr(); got != tt.want {
				t.Errorf("Addr() = %q, want %q", got, tt.want)
			}
		})
	}
}

func TestProber_ProbeAll(t *testing.T) {
	t.Parallel()

	lan := probe.Path{Name: "lan", Host: "192.168.3.141"}
	ts := probe.Path{Name: "ts", Host: "100.105.178.123"}
	pub := probe.Path{Name: "public", Host: "203.0.113.150"}

	dial := fakedial.New().
		Up("192.168.3.141:22", 20*time.Millisecond).
		Down("100.105.178.123:22").
		Up("203.0.113.150:22", 70*time.Millisecond)

	got := probe.Prober{Dialer: dial}.ProbeAll(context.Background(), []probe.Path{lan, ts, pub})

	if len(got) != 3 {
		t.Fatalf("ProbeAll() returned %d statuses, want 3", len(got))
	}
	// Input order is preserved even though ts finished first and pub is slowest.
	wantNames := []string{"lan", "ts", "public"}
	for i, want := range wantNames {
		if got[i].Path.Name != want {
			t.Errorf("status[%d].Path.Name = %q, want %q", i, got[i].Path.Name, want)
		}
	}
	if !got[0].Alive() || got[0].Latency != 20*time.Millisecond {
		t.Errorf("lan = %+v, want alive at 20ms", got[0])
	}
	// A dead path is reported, never dropped.
	if got[1].Alive() {
		t.Errorf("ts = %+v, want dead", got[1])
	}
	if !got[2].Alive() {
		t.Errorf("public = %+v, want alive", got[2])
	}
}

func TestProber_ProbeAll_EmptyInput(t *testing.T) {
	t.Parallel()

	got := probe.Prober{Dialer: fakedial.New()}.ProbeAll(context.Background(), nil)
	if len(got) != 0 {
		t.Errorf("ProbeAll(nil) = %v, want empty", got)
	}
}

// Every path must be dialled, so the UI can show the full picture rather than
// stopping at the first success.
func TestProber_ProbeAll_DialsEveryPath(t *testing.T) {
	t.Parallel()

	dial := fakedial.New().
		Up("a:22", time.Millisecond).
		Up("b:22", time.Millisecond).
		Up("c:22", time.Millisecond)

	probe.Prober{Dialer: dial}.ProbeAll(context.Background(), []probe.Path{
		{Name: "a", Host: "a"}, {Name: "b", Host: "b"}, {Name: "c", Host: "c"},
	})

	if got := len(dial.Calls()); got != 3 {
		t.Errorf("dialled %d addresses, want 3", got)
	}
}

// Probing must be concurrent: three 100ms paths should take ~100ms, not 300ms.
func TestProber_ProbeAll_IsConcurrent(t *testing.T) {
	t.Parallel()

	const delay = 100 * time.Millisecond
	dial := fakedial.New()
	paths := make([]probe.Path, 3)
	for i, name := range []string{"a", "b", "c"} {
		paths[i] = probe.Path{Name: name, Host: name}
		dial.Stub(name+":22", fakedial.Reply{Delay: delay})
	}

	start := time.Now()
	probe.Prober{Dialer: dial, Timeout: time.Second}.ProbeAll(context.Background(), paths)
	elapsed := time.Since(start)

	if elapsed > 2*delay {
		t.Errorf("ProbeAll took %v for 3 concurrent %v dials, want well under %v", elapsed, delay, 3*delay)
	}
}

// A hung path must never hold the UI past the timeout.
func TestProber_ProbeAll_HonorsTimeout(t *testing.T) {
	t.Parallel()

	dial := fakedial.New().Stub("slow:22", fakedial.Reply{Delay: 5 * time.Second})

	start := time.Now()
	got := probe.Prober{Dialer: dial, Timeout: 50 * time.Millisecond}.
		ProbeAll(context.Background(), []probe.Path{{Name: "slow", Host: "slow"}})
	elapsed := time.Since(start)

	if elapsed > time.Second {
		t.Errorf("ProbeAll took %v, want to give up near the 50ms timeout", elapsed)
	}
	if got[0].Alive() {
		t.Errorf("timed-out path reported alive: %+v", got[0])
	}
}

func TestProber_ProbeAll_CancelledContext(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	dial := fakedial.New().Up("a:22", time.Millisecond)
	got := probe.Prober{Dialer: dial}.ProbeAll(ctx, []probe.Path{{Name: "a", Host: "a"}})

	if got[0].Alive() {
		t.Errorf("status = %+v, want dead under a cancelled context", got[0])
	}
}

// An unstubbed address must read as down, never as reachable.
func TestProber_ProbeAll_UnstubbedIsDown(t *testing.T) {
	t.Parallel()

	got := probe.Prober{Dialer: fakedial.New()}.
		ProbeAll(context.Background(), []probe.Path{{Name: "a", Host: "a"}})

	if got[0].Alive() {
		t.Error("unstubbed address reported alive")
	}
	if !errors.Is(got[0].Err, fakedial.ErrUnstubbed) {
		t.Errorf("Err = %v, want ErrUnstubbed", got[0].Err)
	}
}

func TestSelect(t *testing.T) {
	t.Parallel()

	dead := errors.New("refused")
	lan := probe.Path{Name: "lan"}
	ts := probe.Path{Name: "ts"}
	pub := probe.Path{Name: "public"}

	tests := []struct {
		name     string
		statuses []probe.Status
		want     string
		wantOK   bool
	}{
		{
			name: "first alive in configured order wins",
			statuses: []probe.Status{
				{Path: lan, Latency: 20 * time.Millisecond},
				{Path: ts, Latency: time.Millisecond},
			},
			want: "lan", wantOK: true,
		},
		{
			name: "order is preference, not latency",
			statuses: []probe.Status{
				{Path: lan, Latency: 900 * time.Millisecond},
				{Path: ts, Latency: time.Millisecond},
			},
			want: "lan", wantOK: true,
		},
		{
			name: "dead paths are skipped",
			statuses: []probe.Status{
				{Path: lan, Err: dead},
				{Path: ts, Err: dead},
				{Path: pub, Latency: 70 * time.Millisecond},
			},
			want: "public", wantOK: true,
		},
		{
			name: "all dead selects nothing",
			statuses: []probe.Status{
				{Path: lan, Err: dead},
				{Path: ts, Err: dead},
			},
			wantOK: false,
		},
		{name: "empty selects nothing", statuses: nil, wantOK: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, ok := probe.Select(tt.statuses)
			if ok != tt.wantOK {
				t.Fatalf("Select() ok = %v, want %v", ok, tt.wantOK)
			}
			if ok && got.Path.Name != tt.want {
				t.Errorf("Select() = %q, want %q", got.Path.Name, tt.want)
			}
		})
	}
}

func TestFastest(t *testing.T) {
	t.Parallel()

	dead := errors.New("refused")
	statuses := []probe.Status{
		{Path: probe.Path{Name: "lan"}, Latency: 20 * time.Millisecond},
		{Path: probe.Path{Name: "ts"}, Err: dead},
		{Path: probe.Path{Name: "public"}, Latency: 5 * time.Millisecond},
	}

	got, ok := probe.Fastest(statuses)
	if !ok || got.Path.Name != "public" {
		t.Errorf("Fastest() = %q/%v, want public/true", got.Path.Name, ok)
	}

	// A dead path must never win on its zero latency.
	if _, ok := probe.Fastest([]probe.Status{{Path: probe.Path{Name: "ts"}, Err: dead}}); ok {
		t.Error("Fastest() found a live path among only dead ones")
	}
}

func TestFastest_TieGoesToEarlierPath(t *testing.T) {
	t.Parallel()

	statuses := []probe.Status{
		{Path: probe.Path{Name: "lan"}, Latency: 20 * time.Millisecond},
		{Path: probe.Path{Name: "ts"}, Latency: 20 * time.Millisecond},
	}
	if got, _ := probe.Fastest(statuses); got.Path.Name != "lan" {
		t.Errorf("Fastest() = %q, want lan", got.Path.Name)
	}
}

func TestAlive(t *testing.T) {
	t.Parallel()

	dead := errors.New("refused")
	statuses := []probe.Status{
		{Path: probe.Path{Name: "lan"}},
		{Path: probe.Path{Name: "ts"}, Err: dead},
		{Path: probe.Path{Name: "public"}},
	}

	var names []string
	for _, s := range probe.Alive(statuses) {
		names = append(names, s.Path.Name)
	}
	if want := []string{"lan", "public"}; !reflect.DeepEqual(names, want) {
		t.Errorf("Alive() = %v, want %v", names, want)
	}
}

// Net is the real dialer; exercise it against a real listener so the seam is
// not the only thing ever tested.
func TestNet_Dial(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() err = %v", err)
	}
	defer ln.Close()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			_ = conn.Close()
		}
	}()

	if _, err := (probe.Net{}).Dial(context.Background(), ln.Addr().String()); err != nil {
		t.Errorf("Dial(open port) err = %v, want nil", err)
	}
}

func TestNet_Dial_ClosedPort(t *testing.T) {
	t.Parallel()

	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() err = %v", err)
	}
	addr := ln.Addr().String()
	ln.Close() // nothing is listening there now

	if _, err := (probe.Net{}).Dial(context.Background(), addr); err == nil {
		t.Error("Dial(closed port) err = nil, want refused")
	}
}
