package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The wordmark (SASASH.4.29): a slanted figlet face — speed is the
// product — with a chevron wake in the secondary accent. It replaces the
// one-line title only when the terminal has the rows and columns for it;
// a short or narrow terminal keeps the plain title, so the panes never
// pay for decoration they cannot afford. Under NO_COLOR the face is
// still ascii and the wake still reads as motion.

// logoFace is one wordmark: figlet 2.2.5 output for "sasash" in the
// named font (rows padded to one width), and the wake — chevrons per
// row, the middle rows longest so the baseline stays clear for the tally.
type logoFace struct {
	name string
	rows []string
	wake []int
}

// logoFaces are the shipped faces; logoFaceName picks one and is a
// string so a demo build can select another with -ldflags -X.
var logoFaces = []logoFace{
	{name: "slant", wake: []int{0, 2, 4, 2, 0}, rows: []string{
		"                               __  ",
		"   _________ __________ ______/ /_ ",
		"  / ___/ __ `/ ___/ __ `/ ___/ __ \\",
		" (__  ) /_/ (__  ) /_/ (__  ) / / /",
		"/____/\\__,_/____/\\__,_/____/_/ /_/ ",
	}},
	{name: "smslant", wake: []int{0, 2, 4, 0}, rows: []string{
		"                         __ ",
		"  ___ ___ ____ ___ ____ / / ",
		" (_-</ _ `(_-</ _ `(_-</ _ \\",
		"/___/\\_,_/___/\\_,_/___/_//_/",
	}},
	{name: "speed", wake: []int{0, 2, 4, 2, 0}, rows: []string{
		"                                   ______  ",
		"_____________ _____________ __________  /_ ",
		"__  ___/  __ `/_  ___/  __ `/_  ___/_  __ \\",
		"_(__  )/ /_/ /_(__  )/ /_/ /_(__  )_  / / /",
		"/____/ \\__,_/ /____/ \\__,_/ /____/ /_/ /_/ ",
	}},
}

var logoFaceName = "slant"

// minLogoHeight is the shortest terminal that shows the wordmark. It
// costs four rows over the plain title; a full-screen terminal has them
// to spare, a classic 80x24 window or the half of a split does not.
const minLogoHeight = 32

// wakeGap separates the face from its chevrons.
const wakeGap = 2

func currentLogo() logoFace {
	for _, f := range logoFaces {
		if f.name == logoFaceName {
			return f
		}
	}
	return logoFaces[0]
}

// logoWidth is the columns the wordmark takes, wake included.
func logoWidth(f logoFace) int {
	w := 0
	for i, r := range f.rows {
		if n := len(r) + wakeGap + f.wake[i]; n > w {
			w = n
		}
	}
	return w
}

// logoFits reports whether the wordmark is shown at this frame size: the
// rows must exist and the widest row (with its wake) must fit whole, as
// must whatever rides the baseline row after it (tally is the header's
// information; the wordmark is not allowed to evict it).
func logoFits(f logoFace, width, height, baseline int) bool {
	return height >= minLogoHeight && logoWidth(f)+1+baseline <= width
}

// renderLogo draws the face in the title color and the wake in the
// secondary accent, one leading blank like the plain title. The last
// row is the baseline: rendered without padding or wake (every face
// declares it wake-free) so the header can append the tally to it.
func renderLogo(st Styles, f logoFace) []string {
	out := make([]string, len(f.rows))
	for i, r := range f.rows {
		line := " " + st.Title.Render(r)
		if k := f.wake[i]; k > 0 {
			line += strings.Repeat(" ", wakeGap) + st.LogoWake.Render(strings.Repeat("»", k))
		}
		out[i] = line
	}
	last := len(out) - 1
	out[last] = " " + st.Title.Render(strings.TrimRight(f.rows[last], " "))
	return out
}

// showLogo reports whether this frame carries the wordmark: it fits
// with the tally beside its baseline.
func (m Model) showLogo() bool {
	baseline := 0
	if sum := m.summaryLine(); sum != "" {
		baseline = 3 + lipgloss.Width(sum)
	}
	return logoFits(currentLogo(), m.width, m.height, baseline)
}

// logoLines is the header's wordmark block for this frame, nil when the
// plain title is the right size.
func (m Model) logoLines() []string {
	if !m.showLogo() {
		return nil
	}
	return renderLogo(m.styles, currentLogo())
}

// headerHeight is the rows above the panes: the title (one line, or the
// wordmark) and the blank under it.
func (m Model) headerHeight() int {
	if m.showLogo() {
		return len(currentLogo().rows) + 1
	}
	return 2
}
