package main

import (
	"fmt"
	"time"

	"github.com/gdamore/tcell/v2"
)

// removalPrompt represents the modal shown when a token is removed from the NFC portal while the
// clear-on-remove option is active. Its keys are handled by ui.handleTokenRemoved since the
// prompt needs a timeout, which the regular modal input flow does not support. The remaining
// time counts down in the prompt and the border flashes red during the final ten seconds.
type removalPrompt struct {
	*modal
	remaining int // Remaining time in seconds, shown in the prompt.
	flash     bool
}

// newRemovalPrompt creates a new removalPrompt struct ready for use.
func newRemovalPrompt(s tcell.Screen, log chan<- []byte) *removalPrompt {
	r := &removalPrompt{remaining: int(removalTimeout / time.Second)}
	r.modal = newModal(
		s,
		boxOpts{title: "token removed", xPos: -1, yPos: -1, width: 66, height: 10, typ: boxTypeCharacter, needAmiibo: true},
		nil,
		r.draw,
		r.resetState,
		log,
	)

	return r
}

// draw will handle displaying of the modal content.
func (r *removalPrompt) draw(x, y int) {
	lines := []string{
		"The token was removed from the NFC portal.",
		"",
		"s: save the amiibo to disk before clearing the view",
		"c or ESC: clear the view right away",
	}

	style := tcell.StyleDefault.Background(backColour).Foreground(fontColour)
	for i, l := range lines {
		for j, char := range l {
			r.s.SetContent(x+1+j, y+1+i, char, nil, style)
		}
	}
	r.drawCountdown()
	r.s.Show()
}

// drawCountdown renders the countdown line of the prompt.
func (r *removalPrompt) drawCountdown() {
	x, y := r.getXY()
	style := tcell.StyleDefault.Background(backColour).Foreground(fontColour)
	if r.remaining <= 10 {
		style = style.Foreground(tcell.ColorRed).Attributes(tcell.AttrBold)
	}

	line := fmt.Sprintf("The view clears automatically in %d seconds.   ", r.remaining)
	for j, char := range line {
		r.s.SetContent(x+2+j, y+7, char, nil, style)
	}
}

// tick updates the countdown and flashes the title row to grab attention, escalating to the full
// border during the final ten seconds. It is called from the token removal interaction loop on
// every ticker event.
func (r *removalPrompt) tick(rem time.Duration) {
	if !r.active {
		return
	}

	secs := int((rem + time.Second - 1) / time.Second)
	if secs != r.remaining {
		r.remaining = secs
		r.drawCountdown()
	}

	r.flash = !r.flash
	if r.remaining <= 10 {
		r.paintBorder(r.flash)
	} else {
		r.paintBorder(false)
		r.paintTop(r.flash)
	}

	r.s.Show()
}

// flashStyle returns the style to repaint prompt chrome with.
func flashStyle(flash bool) tcell.Style {
	style := tcell.StyleDefault.Background(backColour).Foreground(fontColour)
	if flash {
		style = style.Foreground(tcell.ColorRed).Attributes(tcell.AttrBold)
	}
	return style
}

// repaint restyles a single cell, preserving its rune.
func (r *removalPrompt) repaint(cx, cy int, style tcell.Style) {
	mainc, combc, _, _ := r.s.GetContent(cx, cy)
	r.s.SetContent(cx, cy, mainc, combc, style)
}

// paintTop repaints the top border row holding the title, in bold red when flash is true and in
// the default style otherwise.
func (r *removalPrompt) paintTop(flash bool) {
	style := flashStyle(flash)
	x, y := r.getXY()
	for i := 0; i < r.width(); i++ {
		r.repaint(x+i, y, style)
	}
}

// paintBorder repaints the full border of the prompt, in bold red when flash is true and in the
// default style otherwise. The border runes themselves are preserved.
func (r *removalPrompt) paintBorder(flash bool) {
	style := flashStyle(flash)
	x, y := r.getXY()
	w, h := r.width(), r.height()

	for i := 0; i < w; i++ {
		r.repaint(x+i, y, style)
		r.repaint(x+i, y+h-1, style)
	}
	for j := 1; j < h-1; j++ {
		r.repaint(x, y+j, style)
		r.repaint(x+w-1, y+j, style)
	}
}

// resetState restores the initial countdown state when the prompt deactivates.
func (r *removalPrompt) resetState() {
	r.remaining = int(removalTimeout / time.Second)
	r.flash = false
}
