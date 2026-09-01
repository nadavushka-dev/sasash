// UI-behavior tests through a real (headless) tea.Program via teatest —
// the tier-2 harness from RESEARCH-tui-automation.md. Assertions are
// conditions (WaitFor on substrings) and typed FinalModel state, never
// full-frame goldens; RequireEqualOutput is deliberately unused (SPEC §12
// addendum). Seam logic (argv, probing, config) stays with the seam tests;
// this file covers only what needs the real event loop: async probe →
// render, key routing, filter gating, quit.
package ui_test

import (
	"bytes"
	"testing"
	"time"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/exp/teatest"

	"github.com/nadavushka-dev/sasash/internal/hostmodel"
	"github.com/nadavushka-dev/sasash/internal/probe"
	"github.com/nadavushka-dev/sasash/internal/probe/fakedial"
	"github.com/nadavushka-dev/sasash/internal/run/fakerun"
	"github.com/nadavushka-dev/sasash/internal/ui"
)

const teatestTimeout = 3 * time.Second

// programModel builds the model teatest drives: alpha up (with a dialer
// delay so the probe genuinely lands asynchronously), bravo down.
//
// The aliases end in .invalid (RFC 2606) on purpose: enter is one
// filter-guard bug away from tea.ExecProcess running `ssh <alias>` for
// real, and an unresolvable alias caps that blast radius at nothing.
// These tests deliberately do NOT use ui.StaticCursor — under a real
// program the cursor blink runs on its own goroutine and never blocks,
// and keeping it is part of the real-loop fidelity this tier exists for.
func programModel() ui.Model {
	hosts := []hostmodel.Host{
		{Alias: "alpha.invalid", Tags: []string{"dev"}, Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}},
		{Alias: "bravo.invalid", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.20"}}},
	}
	dialer := fakedial.New().
		Stub("192.0.2.10:22", fakedial.Reply{Latency: 20 * time.Millisecond, Delay: 30 * time.Millisecond}).
		Down("192.0.2.20:22")
	return ui.New(hosts, probe.Prober{Dialer: dialer, Timeout: time.Second}, fakerun.New(), ui.Config{})
}

func newProgram(t *testing.T) *teatest.TestModel {
	t.Helper()
	return teatest.NewTestModel(t, programModel(), teatest.WithInitialTermSize(80, 24))
}

// waitFor polls the program's output until every want has appeared.
// tm.Output() is a DRAINING reader — each call consumes what earlier
// frames wrote — so substrings expected in the same frame must go into
// one call, never into consecutive ones.
func waitFor(t *testing.T, tm *teatest.TestModel, wants ...string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		for _, want := range wants {
			if !bytes.Contains(bts, []byte(want)) {
				return false
			}
		}
		return true
	}, teatest.WithDuration(teatestTimeout))
}

// finalModel quits the program via q and returns the typed model.
func finalModel(t *testing.T, tm *teatest.TestModel) ui.Model {
	t.Helper()
	tm.Type("q")
	fm, ok := tm.FinalModel(t, teatest.WithFinalTimeout(teatestTimeout)).(ui.Model)
	if !ok {
		t.Fatalf("FinalModel() is not a ui.Model")
	}
	return fm
}

func TestModel_Program_AsyncProbeRenders(t *testing.T) {
	t.Parallel()
	tm := newProgram(t)
	// The probe lands through the real event loop after the dialer delay:
	// alpha's latency and bravo's down state both reach the screen.
	waitFor(t, tm, "20ms")
	tm.Type("j")
	waitFor(t, tm, "down")
	fm := finalModel(t, tm)
	if host, ok := fm.Selected(); !ok || host.Alias != "bravo.invalid" {
		t.Errorf("Selected() = %v, %v; want bravo.invalid after j", host.Alias, ok)
	}
}

func TestModel_Program_KeyRoutingToList(t *testing.T) {
	t.Parallel()
	tm := newProgram(t)
	waitFor(t, tm, "alpha")
	tm.Type("j")
	waitFor(t, tm, "ssh bravo") // preview follows the selection
	tm.Type("k")
	waitFor(t, tm, "ssh alpha")
	fm := finalModel(t, tm)
	if host, ok := fm.Selected(); !ok || host.Alias != "alpha.invalid" {
		t.Errorf("Selected() = %v, %v; want alpha.invalid after j k", host.Alias, ok)
	}
}

func TestModel_Program_FilterGatesQuit(t *testing.T) {
	t.Parallel()
	tm := newProgram(t)
	waitFor(t, tm, "alpha")
	// While the filter input is focused, q is text, not quit: the program
	// must still be running and showing the typed term afterwards.
	tm.Type("/")
	waitFor(t, tm, "apply filter") // filter-mode help proves focus
	tm.Type("q")
	waitFor(t, tm, "q") // the term reached the input, not the quit path
	tm.Send(tea.KeyMsg{Type: tea.KeyEsc})
	waitFor(t, tm, "yank host") // full app help back: filtering cancelled
	fm := finalModel(t, tm)
	if got := ui.FilterState(fm); got != list.Unfiltered {
		t.Errorf("FilterState() = %v, want Unfiltered after esc", got)
	}
	if _, ok := fm.Selected(); !ok {
		t.Errorf("Selected() ok = false, want a selection after cancelled filter")
	}
}

func TestModel_Program_FilterByTagThenConnectPreview(t *testing.T) {
	t.Parallel()
	tm := newProgram(t)
	waitFor(t, tm, "alpha")
	// Move off alpha first: bubbletea repaints diffs, so the preview
	// asserting the tag match must actually CHANGE to be re-emitted.
	tm.Type("j")
	waitFor(t, tm, "ssh bravo")
	tm.Type("/dev")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // accept the filter
	// One wait, one repaint: applying the filter snaps the selection to
	// the tag match, so the header term and the preview change together.
	waitFor(t, tm, "/dev", "ssh alpha")
	fm := finalModel(t, tm)
	if host, ok := fm.Selected(); !ok || host.Alias != "alpha.invalid" {
		t.Errorf("Selected() = %v, %v; want alpha.invalid via tag filter", host.Alias, ok)
	}
	if got := ui.FilterState(fm); got != list.FilterApplied {
		t.Errorf("FilterState() = %v, want FilterApplied", got)
	}
}

// TestModel_Program_ThemeCycle covers only what needs the loop: the t
// keypress routing through the real program. Style wiring belongs to
// theme_test.go and is deliberately not re-asserted here.
func TestModel_Program_ThemeCycle(t *testing.T) {
	t.Parallel()
	tm := newProgram(t)
	waitFor(t, tm, "alpha")
	tm.Type("t")
	waitFor(t, tm, "theme: mocha")
	fm := finalModel(t, tm)
	if got := ui.ThemeName(fm); got != "mocha" {
		t.Errorf("ThemeName() = %v, want mocha", got)
	}
}

func TestModel_Program_QuitCleanly(t *testing.T) {
	t.Parallel()
	tm := newProgram(t)
	waitFor(t, tm, "alpha")
	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(teatestTimeout))
}
