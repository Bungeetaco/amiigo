package main

import (
	"github.com/gdamore/tcell/v2"
)

// removalPrompt represents the modal shown when a token is removed from the NFC portal while the
// clear-on-remove option is active. Its keys are handled by ui.handleTokenRemoved since the
// prompt needs a timeout, which the regular modal input flow does not support.
type removalPrompt struct {
	*modal
}

// newRemovalPrompt creates a new removalPrompt struct ready for use.
func newRemovalPrompt(s tcell.Screen, log chan<- []byte) *removalPrompt {
	r := &removalPrompt{}
	r.modal = newModal(
		s,
		boxOpts{title: "token removed", xPos: -1, yPos: -1, width: 66, height: 10, typ: boxTypeCharacter, needAmiibo: true},
		nil,
		r.draw,
		nil,
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
		"",
		"The view clears automatically after 30 seconds.",
	}

	style := tcell.StyleDefault.Background(backColour).Foreground(fontColour)
	for i, l := range lines {
		for j, char := range l {
			r.s.SetContent(x+1+j, y+1+i, char, nil, style)
		}
	}
	r.s.Show()
}
