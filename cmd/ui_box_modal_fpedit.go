package main

import (
	"github.com/gdamore/tcell/v2"
	"github.com/malc0mn/amiigo/amiibo"
)

// fpEditSubmitHandler defines a submit handler for an fpEditModal, receiving the full modified
// dump in the amiitool (internal) layout.
type fpEditSubmitHandler func(internal []byte, amb *amb, log chan<- []byte) bool

// fpEditModal represents a modal for structured editing of Super Smash Bros. Ultimate figure
// player data, based on the region definitions of the smash-amiibo-editor project.
type fpEditModal struct {
	*modal
	internal []byte // The decrypted dump in amiitool (internal) layout.
	orig     []byte
	sel      int
	offset   int
	fail     string
	initable bool // True when the fail state can be resolved by initializing SSBU data.
	submit   fpEditSubmitHandler
}

// newFPEditModal creates a new fpEditModal struct ready for use.
func newFPEditModal(s tcell.Screen, opts boxOpts, log chan<- []byte, submit fpEditSubmitHandler) *fpEditModal {
	f := &fpEditModal{submit: submit}
	f.modal = newModal(s, opts, f.handleInput, f.drawModalContent, f.reset, log)

	return f
}

// loadBuf decrypts a copy of the active amiibo and converts it to the internal layout the SSBU
// region definitions use.
func (f *fpEditModal) loadBuf() {
	f.fail = ""
	f.initable = false

	if err := loadSSBURegions(); err != nil {
		f.fail = "Cannot load SSBU region definitions: " + err.Error()
		return
	}

	if f.amb == nil || f.amb.a == nil {
		f.fail = "No amiibo data!"
		return
	}
	if conf.retailKey == nil {
		f.fail = "Cannot edit figure player: no retail key loaded."
		return
	}

	cp, err := amiibo.NewAmiidump(f.amb.a.Raw(), f.amb.a.Type())
	if err != nil {
		f.fail = err.Error()
		return
	}
	dec := cp
	if !f.amb.dec {
		if dec, err = amiibo.Decrypt(conf.retailKey, cp); err != nil {
			f.fail = "Decryption error: " + err.Error()
			return
		}
	}

	if !amiibo.HasSSBUData(dec) {
		f.fail = "This amiibo holds no SSBU data. Press 'i' to initialize it as a new figure player, ESC to abort."
		f.initable = true
		f.stash(dec)
		return
	}

	f.stash(dec)
}

// stash stores the internal layout representation of the given decrypted dump.
func (f *fpEditModal) stash(dec amiibo.Amiidump) {
	var t *amiibo.Amiitool
	if dec.Type() == amiibo.TypeAmiitool {
		t, _ = amiibo.NewAmiitool(dec.Raw(), nil)
	} else {
		a, _ := amiibo.NewAmiibo(dec.Raw(), nil)
		t = amiibo.AmiiboToAmiitool(a)
	}

	f.internal = make([]byte, len(t.Raw()))
	copy(f.internal, t.Raw())
	f.orig = make([]byte, len(f.internal))
	copy(f.orig, f.internal)
}

// handleInput will handle keyboard input for the fpEditModal.
func (f *fpEditModal) handleInput(e *tcell.EventKey) {
	if f.fail != "" {
		if f.initable && (e.Rune() == 'i' || e.Rune() == 'I') {
			t, err := amiibo.NewAmiitool(f.internal, nil)
			if err != nil {
				return
			}
			amiibo.InitializeSSBU(t)
			f.log <- encodeStringCell("Initialized amiibo as a new SSBU figure player")
			f.stash(t)
			f.fail = ""
			f.redrawContent()
		}
		return
	}

	switch {
	case e.Key() == tcell.KeyUp:
		f.move(-1)
	case e.Key() == tcell.KeyDown:
		f.move(1)
	case e.Key() == tcell.KeyPgUp:
		f.move(-f.pageRows())
	case e.Key() == tcell.KeyPgDn:
		f.move(f.pageRows())
	case e.Key() == tcell.KeyHome:
		f.move(-len(ssbuRegions))
	case e.Key() == tcell.KeyEnd:
		f.move(len(ssbuRegions))
	case e.Key() == tcell.KeyLeft:
		f.stepSel(-1)
	case e.Key() == tcell.KeyRight:
		f.stepSel(1)
	case e.Rune() == '[':
		f.stepSel(-10)
	case e.Rune() == ']':
		f.stepSel(10)
	case e.Rune() == '{':
		f.stepSel(-1000)
	case e.Rune() == '}':
		f.stepSel(1000)
	case e.Rune() == 'z' || e.Rune() == 'Z':
		r := &ssbuRegions[f.sel]
		min, _ := r.bounds()
		r.setRawValue(f.internal, min)
		f.redrawContent()
	case e.Rune() == 'x' || e.Rune() == 'X':
		r := &ssbuRegions[f.sel]
		_, max := r.bounds()
		r.setRawValue(f.internal, max)
		f.redrawContent()
	case e.Key() == tcell.KeyEnter || e.Rune() == '\n':
		internal := make([]byte, len(f.internal))
		copy(internal, f.internal)
		if f.submit(internal, f.amb, f.log) {
			// Signal the modal is done.
			f.end()
		}
	}
}

// move shifts the selection by n regions, keeping it within bounds.
func (f *fpEditModal) move(n int) {
	f.sel += n
	if f.sel < 0 {
		f.sel = 0
	}
	if f.sel > len(ssbuRegions)-1 {
		f.sel = len(ssbuRegions) - 1
	}
	f.redrawContent()
}

// stepSel changes the value of the selected region by n steps.
func (f *fpEditModal) stepSel(n int64) {
	ssbuRegions[f.sel].step(f.internal, n)
	f.redrawContent()
}

// pageRows returns the number of visible region rows.
func (f *fpEditModal) pageRows() int {
	// Top border, hint line, blank line, then rows, then blank line, two description lines and
	// the bottom border.
	rows := f.height() - 8
	if rows < 1 {
		rows = 1
	}
	return rows
}

// drawModalContent will handle displaying of the modal content.
func (f *fpEditModal) drawModalContent(_, _ int) {
	if f.internal == nil {
		f.loadBuf()
	}
	f.redrawContent()
}

// redrawContent redraws the region list and the description of the selected region.
func (f *fpEditModal) redrawContent() {
	x, y := f.getXY()
	left := x + 2
	top := y + 1
	right := x + f.width() - 3
	bottom := y + f.height() - 2
	base := tcell.StyleDefault.Background(backColour).Foreground(fontColour)

	// Clear the content area.
	for cy := top; cy <= bottom; cy++ {
		for cx := left; cx <= right; cx++ {
			f.s.SetContent(cx, cy, ' ', nil, base)
		}
	}

	if f.fail != "" {
		f.drawStr(left, top, trunc(f.fail, right-left+1), base)
		f.s.Show()
		return
	}

	f.drawStr(left, top, "←/→ [ ] { } change value, z min, x max, ENTER apply, ESC abort", base.Attributes(tcell.AttrDim))

	rows := f.pageRows()
	if f.sel < f.offset {
		f.offset = f.sel
	}
	if f.sel > f.offset+rows-1 {
		f.offset = f.sel - rows + 1
	}

	changed := base.Foreground(tcell.ColorYellow).Attributes(tcell.AttrBold)
	valWidth := 24
	nameWidth := right - left + 1 - valWidth - 2

	for i := 0; i < rows && f.offset+i < len(ssbuRegions); i++ {
		r := &ssbuRegions[f.offset+i]
		ry := top + 2 + i

		style := base
		if r.rawValue(f.internal) != r.rawValue(f.orig) {
			style = changed
		}
		if f.offset+i == f.sel {
			style = style.Attributes(tcell.AttrReverse)
			f.drawStr(left, ry, spaces(right-left+1), style)
		}

		f.drawStr(left, ry, trunc(r.name, nameWidth), style)
		val := r.display(f.internal)
		f.drawStr(right-valWidth+1, ry, trunc(val, valWidth), style)
	}

	// Description of the selected region at the bottom.
	desc := ssbuRegions[f.sel].description
	w := right - left + 1
	f.drawStr(left, bottom-1, trunc(desc, w), base.Attributes(tcell.AttrDim))
	if len(desc) > w {
		f.drawStr(left, bottom, trunc(desc[w:], w), base.Attributes(tcell.AttrDim))
	}

	f.s.Show()
}

// drawStr draws a string on the given position with the given style.
func (f *fpEditModal) drawStr(x, y int, s string, style tcell.Style) {
	for i, r := range []rune(s) {
		f.s.SetContent(x+i, y, r, nil, style)
	}
}

// spaces returns a string of n spaces.
func spaces(n int) string {
	s := make([]byte, n)
	for i := range s {
		s[i] = ' '
	}
	return string(s)
}

// reset resets the inner modal state.
func (f *fpEditModal) reset() {
	f.internal = nil
	f.orig = nil
	f.sel = 0
	f.offset = 0
	f.fail = ""
	f.initable = false
}
