package main

import (
	"fmt"
	"strings"

	"github.com/gdamore/tcell/v2"
)

// toggleActionLabel is the label of the clear-on-remove toggle entry, which is rendered with a
// live checkbox reflecting the current state.
const toggleActionLabel = "t: "

// actionsBox is a box displaying the available actions. The entries are laid out in as many
// columns as the box width allows, so on a wide terminal everything fits on one page. What does
// not fit is split into pages which can be flipped with the left and right arrow keys; the title
// shows the current page.
type actionsBox struct {
	*box
	entries []string // Label and description pairs, same format as fixedContent.
	pg      int
}

// newActionsBox creates a new actionsBox struct ready for display on screen.
func newActionsBox(s tcell.Screen, opts boxOpts, entries []string) *actionsBox {
	opts.fixedContent = entries
	a := &actionsBox{entries: entries}
	a.box = newBox(s, opts)

	// Rebuild the buffer with the current page before every draw, so resizes recalculate the
	// column layout and pagination. Called with the box lock held.
	a.redraw = func() {
		a.pg = a.clampPage(a.pg)
		title := "actions"
		if a.pages() > 1 {
			title = fmt.Sprintf("actions %d/%d", a.pg+1, a.pages())
		}
		a.opts.title = title
		a.sbbStart = 0
		a.buffer.Reset()
		a.buffer.Write(a.encodePage())
	}

	return a
}

// numEntries returns the number of label and description pairs.
func (a *actionsBox) numEntries() int {
	return len(a.entries) / 2
}

// entryWidth returns the display width of the entry with the given pair index.
func (a *actionsBox) entryWidth(i int) int {
	w := len(a.entries[i*2]) + len(a.entries[i*2+1])
	if a.entries[i*2] == toggleActionLabel {
		w += 4 // The checkbox: "[x] "
	}
	return w
}

// colWidth returns the width of a layout column: the widest entry plus spacing.
func (a *actionsBox) colWidth() int {
	max := 0
	for i := 0; i < a.numEntries(); i++ {
		if w := a.entryWidth(i); w > max {
			max = w
		}
	}
	return max + 2
}

// cols returns the number of columns that fit the box.
func (a *actionsBox) cols() int {
	marginLeft, marginRight, _, _ := a.bounds()
	c := (marginRight - marginLeft) / a.colWidth()
	if c < 1 {
		c = 1
	}
	return c
}

// capacity returns the number of entries a single page can display.
func (a *actionsBox) capacity() int {
	rows := a.pageSize()
	if rows < 1 {
		rows = 1
	}
	return rows * a.cols()
}

// pages returns the number of pages needed to display all entries.
func (a *actionsBox) pages() int {
	cap := a.capacity()
	n := (a.numEntries() + cap - 1) / cap
	if n < 1 {
		n = 1
	}
	return n
}

// clampPage keeps a page number within bounds, which can change on resize.
func (a *actionsBox) clampPage(pg int) int {
	if last := a.pages() - 1; pg > last {
		return last
	}
	if pg < 0 {
		return 0
	}
	return pg
}

// encodePage encodes the current page in a column major layout: entries fill a column top to
// bottom before moving to the next one.
func (a *actionsBox) encodePage() []byte {
	rows := a.pageSize()
	if rows < 1 {
		rows = 1
	}
	cols := a.cols()
	colW := a.colWidth()

	start := a.pg * a.capacity()
	end := start + a.capacity()
	if end > a.numEntries() {
		end = a.numEntries()
	}

	// Balance the columns: with 12 entries in 2 columns show 6 and 6 instead of a full first
	// column and a short second one.
	if balanced := (end - start + cols - 1) / cols; balanced < rows {
		rows = balanced
	}

	var res []byte
	for r := 0; r < rows; r++ {
		any := false
		var line []byte
		for c := 0; c < cols; c++ {
			i := start + c*rows + r
			if i >= end {
				break
			}
			pad := 0
			if c < cols-1 {
				pad = colW - a.entryWidth(i)
			}
			line = append(line, a.encodeEntry(i, pad)...)
			any = true
		}
		if !any {
			break
		}
		res = append(res, line...)
		res = append(res, encodeStringCell("")...) // Newline.
	}
	return res
}

// encodeEntry encodes a single entry without a trailing newline, padded to the given width. The
// clear-on-remove toggle entry is rendered with a checkbox: yellow and checked when the option
// is enabled, the normal colour and unchecked when disabled.
func (a *actionsBox) encodeEntry(i, pad int) []byte {
	label, text := a.entries[i*2], a.entries[i*2+1]

	var res []byte
	res = append(res, encodeStringCellWithAttrs(label, tcell.AttrBold|tcell.AttrDim, "")...)

	if label == toggleActionLabel {
		mark, colour, attrs := "[ ] ", fontColour, tcell.AttrNone
		if conf.ui.clearOnRemove {
			mark, colour, attrs = "[x] ", tcell.ColorYellow, tcell.AttrBold
		}
		res = append(res, encodeStringCellColour(mark+text, colour, attrs, "")...)
	} else {
		res = append(res, encodeStringCellWithAttrs(text, tcell.AttrNone, "")...)
	}

	if pad > 0 {
		res = append(res, encodeStringCellWithAttrs(strings.Repeat(" ", pad), tcell.AttrNone, "")...)
	}
	return res
}

// flip changes the displayed page by n, wrapping around, and redraws the box.
func (a *actionsBox) flip(n int) {
	pages := a.pages()
	a.pg = (a.pg + n + pages) % pages

	// Blank the content area first: the pages can have different lengths and the draw path does
	// not clear lines it does not render.
	a.clearContent()
	a.draw(false, a.opts.xPos, a.opts.yPos)
	a.s.Show()
}
