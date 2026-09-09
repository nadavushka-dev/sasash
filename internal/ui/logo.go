package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
)

// The wordmark (SASASH.4.29, bolder and on its own band since 4.32): a
// filled figlet face centered across the frame on its own rows, with
// the » wake and the tally on the line under it. It replaces the
// one-line title only when the terminal has the rows and columns for
// it; a short or narrow terminal keeps the plain title, so the panes
// never pay for decoration they cannot afford. The tally has its own
// line, so the wordmark can never evict it. The face is palette-colored
// and reads the same under NO_COLOR: solid glyphs need no color.

// logoFace is one wordmark: figlet 2.2.5 output for "sasash" in the
// named font, rows padded to one width so the block centers as a unit.
type logoFace struct {
	name string
	rows []string
}

// logoFaces are the shipped faces, the default first; logoFaceName picks
// one and is a string so a demo build can select another with -ldflags -X.
var logoFaces = []logoFace{
	// figlet "ANSI Shadow": solid blocks with a drop shadow (6 rows x 48).
	{name: "ansi-shadow", rows: []string{
		"███████╗ █████╗ ███████╗ █████╗ ███████╗██╗  ██╗",
		"██╔════╝██╔══██╗██╔════╝██╔══██╗██╔════╝██║  ██║",
		"███████╗███████║███████╗███████║███████╗███████║",
		"╚════██║██╔══██║╚════██║██╔══██║╚════██║██╔══██║",
		"███████║██║  ██║███████║██║  ██║███████║██║  ██║",
		"╚══════╝╚═╝  ╚═╝╚══════╝╚═╝  ╚═╝╚══════╝╚═╝  ╚═╝",
	}},
	// figlet "ANSI Regular": the same strokes, no shadow (5 rows x 47).
	{name: "ansi-regular", rows: []string{
		"███████  █████  ███████  █████  ███████ ██   ██",
		"██      ██   ██ ██      ██   ██ ██      ██   ██",
		"███████ ███████ ███████ ███████ ███████ ███████",
		"     ██ ██   ██      ██ ██   ██      ██ ██   ██",
		"███████ ██   ██ ███████ ██   ██ ███████ ██   ██",
	}},
	// figlet "Banner3": plain-ascii hashes, the heaviest pure-ascii face (7 rows x 56).
	{name: "banner3", rows: []string{
		" ######     ###     ######     ###     ######  ##     ##",
		"##    ##   ## ##   ##    ##   ## ##   ##    ## ##     ##",
		"##        ##   ##  ##        ##   ##  ##       ##     ##",
		" ######  ##     ##  ######  ##     ##  ######  #########",
		"      ## #########       ## #########       ## ##     ##",
		"##    ## ##     ## ##    ## ##     ## ##    ## ##     ##",
		" ######  ##     ##  ######  ##     ##  ######  ##     ##",
	}},
}

// logoFaceName is the shipped default: ANSI Shadow, the solid face the
// 4.32 pin asked for (the others stay as demo alternates).
var logoFaceName = "ansi-shadow"

// minLogoHeight is the shortest terminal that shows the wordmark. The
// band costs rows over the plain title (six for ANSI Shadow: the face
// and the wake line), so the floor leaves the panes at least 26 rows
// under it; a full-screen terminal has them to spare, a classic 80x24
// window or the half of a split does not.
const minLogoHeight = 36

// wake is the motion motif, leading the tally line under the face.
const wake = "»»»»"

func currentLogo() logoFace {
	for _, f := range logoFaces {
		if f.name == logoFaceName {
			return f
		}
	}
	return logoFaces[0]
}

// logoWidth is the columns the face takes (its rows share one width).
func logoWidth(f logoFace) int {
	w := 0
	for _, r := range f.rows {
		w = max(w, lipgloss.Width(r))
	}
	return w
}

// logoFits reports whether the wordmark is shown at this frame size: the
// rows must exist and the face must fit whole.
func logoFits(f logoFace, width, height int) bool {
	return height >= minLogoHeight && logoWidth(f) <= width
}

// centered pads s to sit mid-frame: half the slack on the left, none on
// the right (the frame is padded by its consumers).
func centered(s string, width int) string {
	if slack := width - lipgloss.Width(s); slack > 1 {
		return strings.Repeat(" ", slack/2) + s
	}
	return s
}

// renderLogo draws the face in the title color, each row centered
// across width. Rows share one width, so the block moves as a unit.
func renderLogo(st Styles, f logoFace, width int) []string {
	out := make([]string, len(f.rows))
	for i, r := range f.rows {
		out[i] = centered(st.Title.Render(r), width)
	}
	return out
}

// showLogo reports whether this frame carries the wordmark band.
func (m Model) showLogo() bool {
	return logoFits(currentLogo(), m.width, m.height)
}

// logoLines is the header's wordmark block for this frame, nil when the
// plain title is the right size.
func (m Model) logoLines() []string {
	if !m.showLogo() {
		return nil
	}
	return renderLogo(m.styles, currentLogo(), m.width)
}

// headerHeight is the rows above the panes: the title line and the
// blank under it — or the wordmark band: the face, the wake-and-tally
// line, and the blank.
func (m Model) headerHeight() int {
	if m.showLogo() {
		return len(currentLogo().rows) + 2
	}
	return 2
}
