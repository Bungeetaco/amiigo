package main

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
)

// actionsBox is a box displaying the available actions. When the list does not fit the box, it
// is split into pages which can be flipped with the left and right arrow keys. The title shows
// the current page.
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
	// pagination. Called with the box lock held.
	a.redraw = func() {
		a.pg = a.clampPage(a.pg)
		a.opts.title = fmt.Sprintf("actions %d/%d", a.pg+1, a.pages())
		a.sbbStart = 0
		a.buffer.Reset()
		a.buffer.Write(a.encodePage(a.pageEntries()))
	}

	return a
}

// toggleActionLabel is the label of the clear-on-remove toggle entry, which is rendered with a
// live checkbox reflecting the current state.
const toggleActionLabel = "t: "

// encodePage encodes the entries of a page like encodeWithLabelToBytes, but renders the
// clear-on-remove toggle entry with a checkbox: yellow and checked when the option is enabled,
// the normal colour and unchecked when disabled.
func (a *actionsBox) encodePage(page []string) []byte {
	var res []byte
	for i := 0; i+1 < len(page); i += 2 {
		label, text := page[i], page[i+1]
		res = append(res, encodeStringCellWithAttrs(label, tcell.AttrBold|tcell.AttrDim, "")...)

		if label == toggleActionLabel {
			mark, colour, attrs := "[ ] ", fontColour, tcell.AttrNone
			if conf.ui.clearOnRemove {
				mark, colour, attrs = "[x] ", tcell.ColorYellow, tcell.AttrBold
			}
			res = append(res, encodeStringCellColour(mark+text, colour, attrs, "\n")...)
			continue
		}

		res = append(res, encodeStringCell(text)...)
	}
	return res
}

// pages returns the number of pages needed to display all entries.
func (a *actionsBox) pages() int {
	rows := a.pageSize()
	if rows < 1 {
		rows = 1
	}
	n := (len(a.entries)/2 + rows - 1) / rows
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

// pageEntries returns the entries of the current page.
func (a *actionsBox) pageEntries() []string {
	rows := a.pageSize()
	if rows < 1 {
		rows = 1
	}

	start := a.pg * rows * 2
	end := start + rows*2
	if start > len(a.entries) {
		start = len(a.entries)
	}
	if end > len(a.entries) {
		end = len(a.entries)
	}
	return a.entries[start:end]
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
