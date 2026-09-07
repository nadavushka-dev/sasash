package main

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/nadavushka-dev/sasash/internal/controlmaster"
	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

// The TUI's ControlMaster-trio hooks (4.18) over the merged writer.

func TestInspectControl_Cases(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		conf    string
		alias   string
		want    ui.ControlStatus
		wantErr error
	}{
		{name: "all missing", conf: "Host old\n  HostName 192.0.2.7\n", alias: "old",
			want: ui.ControlStatus{Missing: []string{"ControlMaster auto", "ControlPath ~/.ssh/cm/%C", "ControlPersist 30m"}}},
		{name: "one missing, other values as written", conf: "Host old\n  HostName 192.0.2.7\n  controlmaster no\n  ControlPath ~/.ssh/other/%C\n", alias: "old",
			want: ui.ControlStatus{Missing: []string{"ControlPersist 30m"}, Other: []string{"ControlMaster no", "ControlPath ~/.ssh/other/%C"}}},
		{name: "none is another value", conf: "Host old\n  ControlMaster auto\n  ControlPath none\n  ControlPersist 30m\n", alias: "old",
			want: ui.ControlStatus{Other: []string{"ControlPath none"}}},
		{name: "complete, values compared without case", conf: "Host old\n  ControlMaster AUTO\n  ControlPath ~/.ssh/cm/%C\n  ControlPersist 30m\n", alias: "old"},
		{name: "not managed", conf: "Host old\n  HostName 192.0.2.7\n", alias: "other", wantErr: ui.ErrNotManaged},
		{name: "an empty alias is nobody's block", conf: "Host\n  HostName 192.0.2.7\n", alias: "", wantErr: ui.ErrNotManaged},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			path := filepath.Join(t.TempDir(), "sasash.conf")
			if err := os.WriteFile(path, []byte(tt.conf), 0o600); err != nil {
				t.Fatal(err)
			}
			got, err := inspectControl(path)(tt.alias)
			if tt.wantErr != nil {
				if !errors.Is(err, tt.wantErr) {
					t.Errorf("inspectControl() err = %v, want %v", err, tt.wantErr)
				}
				return
			}
			if err != nil || !reflect.DeepEqual(got, tt.want) {
				t.Errorf("inspectControl() = %+v, %v; want %+v", got, err, tt.want)
			}
			// The missing lines are the writer's: what Enable would add.
			missing, _ := controlmaster.Missing(path, tt.alias)
			var asLines []string
			for _, d := range missing {
				asLines = append(asLines, d.Keyword+" "+d.Value)
			}
			if !reflect.DeepEqual(got.Missing, asLines) {
				t.Errorf("Missing = %v, want controlmaster's %v", got.Missing, asLines)
			}
		})
	}
	if _, err := inspectControl(filepath.Join(t.TempDir(), "none.conf"))("x"); err == nil {
		t.Errorf("inspectControl() on a missing file = nil, want an error")
	}
}

func TestInspectControl_RefusesAPinnedVariant(t *testing.T) {
	t.Parallel()
	// ubuntu's paths were hand-edited — lan renamed to home, wan's address
	// changed — yet ubuntu.lan and ubuntu.wan sit in the generator's
	// Header run with its shape: they are the generator's, whatever the
	// meta now says. ubuntu.other follows them with a directive of the
	// user's, which breaks the run: a host of the user's.
	conf := "# @sasash path home 192.0.2.77\n# @sasash path wan 203.0.113.99\nHost ubuntu\n  HostName 192.0.2.77\n\n" + pathswitch.Header + "\nHost ubuntu.lan\n  HostName 192.0.2.10\nHost ubuntu.wan\n  HostName 203.0.113.10\nHost ubuntu.other\n  HostName 192.0.2.5\n  ForwardAgent yes\n"
	path := filepath.Join(t.TempDir(), "sasash.conf")
	if err := os.WriteFile(path, []byte(conf), 0o600); err != nil {
		t.Fatal(err)
	}
	for _, alias := range []string{"ubuntu.lan", "ubuntu.wan"} {
		_, err := inspectControl(path)(alias)
		var v *ui.VariantError
		if !errors.As(err, &v) || v.Parent != "ubuntu" || v.Path != alias[len("ubuntu."):] {
			t.Errorf("inspectControl(%s) err = %v, want a VariantError of ubuntu", alias, err)
		}
	}
	if got, err := inspectControl(path)("ubuntu.other"); err != nil || len(got.Missing) != 3 {
		t.Errorf("inspectControl(ubuntu.other) = %+v, %v; want the user's host, all missing", got, err)
	}
	if got, err := inspectControl(path)("ubuntu"); err != nil || len(got.Missing) != 3 {
		t.Errorf("inspectControl(ubuntu) = %+v, %v; want the parent itself, all missing", got, err)
	}
}

func TestEnableControl_WritesThroughTheOneWriter(t *testing.T) {
	t.Parallel()
	path := filepath.Join(t.TempDir(), "sasash.conf")
	before := "Host old\n\tHostName 192.0.2.7\n\tControlPath ~/.ssh/other/%C\n"
	if err := os.WriteFile(path, []byte(before), 0o600); err != nil {
		t.Fatal(err)
	}
	added, err := enableControl(path)("old")
	if err != nil || !reflect.DeepEqual(added, []string{"ControlMaster auto", "ControlPersist 30m"}) {
		t.Fatalf("enableControl() = %v, %v; want the two missing lines", added, err)
	}
	got, _ := os.ReadFile(path)
	if want := "Host old\n\tControlMaster auto\n\tControlPersist 30m\n\tHostName 192.0.2.7\n\tControlPath ~/.ssh/other/%C\n"; string(got) != want {
		t.Errorf("file = %q, want %q", got, want)
	}
	if added, err := enableControl(path)("old"); err != nil || added != nil {
		t.Errorf("second enableControl() = %v, %v; want nothing", added, err)
	}
	if _, err := enableControl(path)("other"); err == nil {
		t.Errorf("enableControl() on an unknown alias = nil, want an error")
	}
}

func TestManagedBlock_RendersTheTrio(t *testing.T) {
	t.Parallel()
	want := "Host edge\n  HostName 192.0.2.99\n  User root\n  Port 2222\n  ControlMaster auto\n  ControlPath ~/.ssh/cm/%C\n  ControlPersist 30m\n"
	if got := managedBlock("edge", "192.0.2.99", "root", "2222"); got != want {
		t.Errorf("managedBlock() = %q, want %q", got, want)
	}
	if got := managedBlock("a1", "192.0.2.5", "", ""); got != "Host a1\n  HostName 192.0.2.5\n  ControlMaster auto\n  ControlPath ~/.ssh/cm/%C\n  ControlPersist 30m\n" {
		t.Errorf("managedBlock() minimal = %q", got)
	}
}
