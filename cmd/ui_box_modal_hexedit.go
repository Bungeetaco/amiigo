package main

import (
	"fmt"

	"github.com/gdamore/tcell/v2"
	"github.com/malc0mn/amiigo/amiibo"
)

// hexEditSubmitHandler defines a submit handler for a hexEditModal, receiving the modified data
// block and an amiibo instance.
type hexEditSubmitHandler func(data []byte, amb *amb, log chan<- []byte) bool

// hexEditModal represents a modal that allows editing the gameplay (application) data of the
// active amiibo as a hex grid.
type hexEditModal struct {
	*modal
	buf    []byte
	orig   []byte
	cur    int // Nibble cursor: byte index * 2 + (0 for high, 1 for low nibble).
	fail   string
	submit hexEditSubmitHandler
}

const hexEditCols = 16

// newHexEditModal creates a new hexEditModal struct ready for use.
func newHexEditModal(s tcell.Screen, opts boxOpts, log chan<- []byte, submit hexEditSubmitHandler) *hexEditModal {
	h := &hexEditModal{submit: submit}
	h.modal = newModal(s, opts, h.handleInput, h.drawModalContent, h.reset, log)

	return h
}

// loadBuf extracts the gameplay data from the active amiibo, decrypting a copy when needed.
func (h *hexEditModal) loadBuf() {
	h.fail = ""

	if h.amb == nil || h.amb.a == nil {
		h.fail = "No amiibo data!"
		return
	}

	a := h.amb.a
	if !h.amb.dec {
		if conf.retailKey == nil {
			h.fail = "Cannot show gameplay data: no retail key loaded."
			return
		}
		cp, err := amiibo.NewAmiidump(a.Raw(), a.Type())
		if err != nil {
			h.fail = err.Error()
			return
		}
		if cp, err = amiibo.Decrypt(conf.retailKey, cp); err != nil {
			h.fail = "Decryption error: " + err.Error()
			return
		}
		a = cp
	}

	h.buf = amiibo.AppData(a)
	h.orig = make([]byte, len(h.buf))
	copy(h.orig, h.buf)
	h.cur = 0
}

// handleInput will handle keyboard input for the hexEditModal.
func (h *hexEditModal) handleInput(e *tcell.EventKey) {
	if h.buf == nil {
		return
	}

	max := len(h.buf)*2 - 1

	switch {
	case e.Key() == tcell.KeyUp:
		h.moveCursor(-2 * hexEditCols)
	case e.Key() == tcell.KeyDown:
		h.moveCursor(2 * hexEditCols)
	case e.Key() == tcell.KeyLeft:
		h.moveCursor(-1)
	case e.Key() == tcell.KeyRight:
		h.moveCursor(1)
	case e.Key() == tcell.KeyHome:
		h.cur = 0
		h.redrawContent()
	case e.Key() == tcell.KeyEnd:
		h.cur = max
		h.redrawContent()
	case e.Key() == tcell.KeyEnter || e.Rune() == '\n':
		data := make([]byte, len(h.buf))
		copy(data, h.buf)
		if h.submit(data, h.amb, h.log) {
			// Signal the modal is done.
			h.end()
		}
	default:
		nibble := hexRuneToNibble(e.Rune())
		if nibble < 0 {
			return
		}
		i := h.cur / 2
		if h.cur%2 == 0 {
			h.buf[i] = h.buf[i]&0x0f | byte(nibble)<<4
		} else {
			h.buf[i] = h.buf[i]&0xf0 | byte(nibble)
		}
		h.moveCursor(1)
	}
}

// moveCursor shifts the nibble cursor by n, keeping it within bounds.
func (h *hexEditModal) moveCursor(n int) {
	h.cur += n
	if h.cur < 0 {
		h.cur = 0
	}
	if max := len(h.buf)*2 - 1; h.cur > max {
		h.cur = max
	}
	h.redrawContent()
}

// hexRuneToNibble converts a hex character to its value, or -1 when the rune is not hex.
func hexRuneToNibble(r rune) int {
	switch {
	case r >= '0' && r <= '9':
		return int(r - '0')
	case r >= 'a' && r <= 'f':
		return int(r-'a') + 10
	case r >= 'A' && r <= 'F':
		return int(r-'A') + 10
	}
	return -1
}

// drawModalContent will handle displaying of the modal content.
func (h *hexEditModal) drawModalContent(_, _ int) {
	if h.buf == nil {
		h.loadBuf()
	}
	h.redrawContent()
}

// redrawContent redraws the hex grid and help line.
func (h *hexEditModal) redrawContent() {
	x, y := h.getXY()
	left := x + 2
	top := y + 1
	right := x + h.width() - 3
	base := tcell.StyleDefault.Background(backColour).Foreground(fontColour)

	if h.fail != "" {
		h.drawStr(left, top, h.fail, base)
		h.s.Show()
		return
	}

	h.drawStr(left, top, "Type hex to edit, ENTER to apply, ESC to abort. Changes are marked.", base.Attributes(tcell.AttrDim))

	changed := base.Foreground(tcell.ColorYellow).Attributes(tcell.AttrBold)
	for row := 0; row*hexEditCols < len(h.buf); row++ {
		ry := top + 2 + row
		h.drawStr(left, ry, fmt.Sprintf("%03X", row*hexEditCols), base.Attributes(tcell.AttrBold|tcell.AttrDim))

		for col := 0; col < hexEditCols && row*hexEditCols+col < len(h.buf); col++ {
			i := row*hexEditCols + col
			bx := left + 5 + col*3
			style := base
			if h.buf[i] != h.orig[i] {
				style = changed
			}

			hi := fmt.Sprintf("%X", h.buf[i]>>4)
			lo := fmt.Sprintf("%X", h.buf[i]&0x0f)
			hiStyle, loStyle := style, style
			if h.cur/2 == i {
				if h.cur%2 == 0 {
					hiStyle = style.Attributes(tcell.AttrReverse)
				} else {
					loStyle = style.Attributes(tcell.AttrReverse)
				}
			}
			h.drawStr(bx, ry, hi, hiStyle)
			h.drawStr(bx+1, ry, lo, loStyle)

			// ASCII column on the right.
			ax := left + 5 + hexEditCols*3 + 2 + col
			if ax <= right {
				c := rune(h.buf[i])
				if c < 32 || c > 126 {
					c = '.'
				}
				h.drawStr(ax, ry, string(c), style)
			}
		}
	}

	h.s.Show()
}

// drawStr draws a string on the given position with the given style.
func (h *hexEditModal) drawStr(x, y int, s string, style tcell.Style) {
	for i, r := range []rune(s) {
		h.s.SetContent(x+i, y, r, nil, style)
	}
}

// reset resets the inner modal state.
func (h *hexEditModal) reset() {
	h.buf = nil
	h.orig = nil
	h.cur = 0
	h.fail = ""
}
