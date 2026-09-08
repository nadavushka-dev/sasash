package tunnel_test

import (
	"context"
	"reflect"
	"testing"
	"time"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakespawn"
)

func TestEngine_Adopt_SurfacesVariantTunnelsUnderTheParent(t *testing.T) {
	t.Parallel()
	s := fakespawn.New().Live(77)
	d := fakedial.New().Up("127.0.0.1:8080", time.Millisecond)
	e := engine(t, s, d)
	// A tunnel recorded under the variant ubuntu1.office (a + pressed while
	// the variant row was selected), and an unrelated one under the parent.
	writeState(t, e, "ubuntu1", `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41}]}`)
	writeState(t, e, "ubuntu1.office", `{"forwards":[{"set":"","spec":"8080:localhost:8080","pid":77}]}`)

	if err := e.Adopt("ubuntu1", []string{"ubuntu1.office", "ubuntu1.tailscale"}); err != nil {
		t.Fatalf("Adopt() err = %v", err)
	}
	// The ad-hoc forward now lives under the parent, keeping its pid; the
	// parent's own record is untouched; the variant file is gone.
	want := `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41},{"set":"","spec":"8080:localhost:8080","pid":77}]}`
	if got := readState(t, e, "ubuntu1"); got != want {
		t.Errorf("parent state = %s, want %s", got, want)
	}
	if got := readState(t, e, "ubuntu1.office"); got != "" {
		t.Errorf("variant state should be removed, got %s", got)
	}
	// Status on the parent now reports the adopted ad-hoc as up (its pid is
	// alive and 8080 answers).
	st, err := e.Status(context.Background(), "ubuntu1", []hostmodel.ForwardSet{{Name: "web", Specs: []string{"3000:localhost:3000"}}})
	if err != nil {
		t.Fatalf("Status() err = %v", err)
	}
	var got []string
	for _, f := range st.Forwards {
		got = append(got, f.Set+"/"+f.Spec+"="+f.State.String())
	}
	want2 := []string{"web/3000:localhost:3000=down", "/8080:localhost:8080=up"}
	if !reflect.DeepEqual(got, want2) {
		t.Errorf("Status() = %q, want %q", got, want2)
	}
}

func TestEngine_Adopt_NoVariantStateWritesNothing(t *testing.T) {
	t.Parallel()
	e := engine(t, fakespawn.New(), fakedial.New())
	writeState(t, e, "ubuntu1", `{"forwards":[{"set":"web","spec":"3000:localhost:3000","pid":41}]}`)
	before := readState(t, e, "ubuntu1")
	if err := e.Adopt("ubuntu1", []string{"ubuntu1.office"}); err != nil {
		t.Fatalf("Adopt() err = %v", err)
	}
	if got := readState(t, e, "ubuntu1"); got != before {
		t.Errorf("Adopt with no variant files rewrote the parent: %s", got)
	}
}

func TestEngine_Adopt_DedupsBySpecParentWins(t *testing.T) {
	t.Parallel()
	e := engine(t, fakespawn.New(), fakedial.New())
	writeState(t, e, "ubuntu1", `{"forwards":[{"set":"","spec":"8080:localhost:8080","pid":41}]}`)
	writeState(t, e, "ubuntu1.office", `{"forwards":[{"set":"","spec":"8080:localhost:8080","pid":99}]}`)
	if err := e.Adopt("ubuntu1", []string{"ubuntu1.office"}); err != nil {
		t.Fatalf("Adopt() err = %v", err)
	}
	want := `{"forwards":[{"set":"","spec":"8080:localhost:8080","pid":41}]}`
	if got := readState(t, e, "ubuntu1"); got != want {
		t.Errorf("parent state = %s, want %s (parent's pid wins)", got, want)
	}
	if got := readState(t, e, "ubuntu1.office"); got != "" {
		t.Errorf("variant file should be removed, got %s", got)
	}
}
