package pathswitch_test

import (
	"testing"

	"github.com/nadavushka-dev/sasash/internal/pathswitch"
	"github.com/nadavushka-dev/sasash/internal/sshconf"
)

func TestGenerated_Cases(t *testing.T) {
	t.Parallel()
	const header = "# pinned variants, generated — for when you want to force one path\n"
	tests := []struct {
		name       string
		conf       string
		alias      string
		wantParent string
		want       bool
	}{
		{name: "anchored variant", conf: "Host ubuntu\n  HostName 192.0.2.10\n\n" + header + "Host ubuntu.lan\n  HostName 192.0.2.10\nHost ubuntu.wan\n  HostName 203.0.113.10\n", alias: "ubuntu.wan", wantParent: "ubuntu", want: true},
		{name: "renamed path leaves the block generated", conf: "# @sasash path home 192.0.2.10\nHost ubuntu\n  HostName 192.0.2.10\n\n" + header + "Host ubuntu.lan\n  HostName 192.0.2.10\n", alias: "ubuntu.lan", wantParent: "ubuntu", want: true},
		{name: "the parent is not generated", conf: "Host ubuntu\n  HostName 192.0.2.10\n\n" + header + "Host ubuntu.lan\n  HostName 192.0.2.10\n", alias: "ubuntu"},
		{name: "a dotted host without the header is the user's", conf: "Host ubuntu\n  HostName 192.0.2.10\n\nHost ubuntu.lan\n  HostName 192.0.2.10\n", alias: "ubuntu.lan"},
		{name: "a directive of the user's breaks the shape", conf: "Host ubuntu\n  HostName 192.0.2.10\n\n" + header + "Host ubuntu.lan\n  HostName 192.0.2.10\n  ForwardAgent yes\n", alias: "ubuntu.lan"},
		{name: "unknown alias", conf: "Host ubuntu\n  HostName 192.0.2.10\n", alias: "other"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := sshconf.Parse("sasash.conf", []byte(tt.conf))
			parent, ok := pathswitch.Generated(f, tt.alias)
			if ok != tt.want || parent != tt.wantParent {
				t.Errorf("Generated(%s) = %q, %v; want %q, %v", tt.alias, parent, ok, tt.wantParent, tt.want)
			}
		})
	}
}
