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
func programModel() ui.Model {
	hosts := []hostmodel.Host{
		{Alias: "alpha", Tags: []string{"dev"}, Paths: []probe.Path{{Name: "lan", Host: "192.0.2.10"}}},
		{Alias: "bravo", Paths: []probe.Path{{Name: "lan", Host: "192.0.2.20"}}},
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

// waitFor polls the program's output for a substring condition.
func waitFor(t *testing.T, tm *teatest.TestModel, want string) {
	t.Helper()
	teatest.WaitFor(t, tm.Output(), func(bts []byte) bool {
		return bytes.Contains(bts, []byte(want))
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
	if host, ok := fm.Selected(); !ok || host.Alias != "bravo" {
		t.Errorf("Selected() = %v, %v; want bravo after j", host.Alias, ok)
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
	if host, ok := fm.Selected(); !ok || host.Alias != "alpha" {
		t.Errorf("Selected() = %v, %v; want alpha after j k", host.Alias, ok)
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
	if _, ok := fm.Selected(); !ok {
		t.Errorf("Selected() ok = false, want a selection after cancelled filter")
	}
}

func TestModel_Program_FilterByTagThenConnectPreview(t *testing.T) {
	t.Parallel()
	tm := newProgram(t)
	waitFor(t, tm, "alpha")
	tm.Type("/dev")
	tm.Send(tea.KeyMsg{Type: tea.KeyEnter}) // accept the filter
	waitFor(t, tm, "/dev")                  // header shows the applied term
	fm := finalModel(t, tm)
	if host, ok := fm.Selected(); !ok || host.Alias != "alpha" {
		t.Errorf("Selected() = %v, %v; want alpha via tag filter", host.Alias, ok)
	}
	if got := ui.FilterState(fm); got.String() != "filter applied" {
		t.Errorf("FilterState() = %v, want filter applied", got)
	}
}

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
	if got := ui.StylesOf(fm).Title.GetForeground(); got != ui.Mocha.Accent {
		t.Errorf("Title foreground = %v, want mocha accent %v", got, ui.Mocha.Accent)
	}
}

func TestModel_Program_QuitCleanly(t *testing.T) {
	t.Parallel()
	tm := newProgram(t)
	waitFor(t, tm, "alpha")
	tm.Type("q")
	tm.WaitFinished(t, teatest.WithFinalTimeout(teatestTimeout))
}
