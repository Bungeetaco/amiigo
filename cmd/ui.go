package main

import (
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gdamore/tcell/v2"
)

// modalOpen counts the interactive elements currently open. While non zero, background drawing
// tones itself down: the reveal animations are skipped and the active modal is redrawn on top.
var modalOpen atomic.Int32

// TODO: is there a way to get rid of this global var?
var amiiboChan chan *amb // amiiboChan is the main channel to pass amb structs around.

// removalTimeout is how long the token removal prompt waits before clearing the view anyway.
// Short, since a cleared amiibo stays in the backup memory and can be restored with b.
const removalTimeout = 10 * time.Second

// removalAlert is the remaining time at which the removal prompt escalates from flashing its
// title to flashing its full border in red.
const removalAlert = 5

// eventTokenRemoved is posted to the tcell event queue when a token is removed from the NFC
// portal, so the main event loop can show the removal prompt. With timedOut set it signals that
// the prompt has expired.
type eventTokenRemoved struct {
	tcell.EventTime
	timedOut bool
}

// newEventTokenRemoved creates a new eventTokenRemoved ready for posting.
func newEventTokenRemoved(timedOut bool) *eventTokenRemoved {
	e := &eventTokenRemoved{timedOut: timedOut}
	e.SetEventNow()
	return e
}

// eventRemovalTick is posted twice per second while the token removal prompt is shown, so it can
// update its countdown and flash its border.
type eventRemovalTick struct {
	tcell.EventTime
	remaining time.Duration
}

// newEventRemovalTick creates a new eventRemovalTick ready for posting.
func newEventRemovalTick(remaining time.Duration) *eventRemovalTick {
	e := &eventRemovalTick{remaining: remaining}
	e.SetEventNow()
	return e
}

// element defines the basic methods which any ui element should implement.
type element interface {
	// activate marks the element as active, so it will process events. The element MUST return nil
	// when activation was unsuccessful.
	// The channel returned can be listened on to see if the box has closed itself.
	activate(amb *amb) <-chan struct{}
	// deactivate deactivates the element, so it will no longer process events.
	deactivate()
	// draw draws the element. When the 'animated' parameter is set to true, the element must be
	// drawn with animation. When the tcell.Screen is refreshed or resized, 'animated' will be
	// false so that the ui is instantly displayed.
	// The return values must be the first x column to the right side of the element and the first
	// y column below the element.
	draw(animated bool, x, y int) (int, int)
	// hasKey must return true if the element is bound to the given rune.
	hasKey(r rune) bool
	// handleKey must act on the given tcell.EventKey.
	handleKey(e *tcell.EventKey)
	// name returns the name of the element.
	name() string
}

// ui holds all the user interface components and the screen to render them on.
type ui struct {
	s        tcell.Screen
	elements []element
	infoBox  *box
	imageBox *imageBox
	usageBox *box
	logBox   *box
	save     element
	removal  *removalPrompt
	actions  *actionsBox
	write    chan []byte
	amb      *amb
	ambNfcId []byte
	backup   *amb   // The last amiibo cleared from the view, restorable with the b action.
	lastName string // Name of the last amiibo resolved through the API, for save suggestions.
	tokenOn  bool   // Whether a token is currently present on the NFC portal.
	active   *modal // The currently open modal, redrawn on top when the view changes beneath it.

	sync.Mutex
}

// setLastName stores the name of the last resolved amiibo in a thread safe way.
func (u *ui) setLastName(name string) {
	u.Lock()
	u.lastName = name
	u.Unlock()
}

// suggestedFilename returns a save filename suggestion based on the last resolved amiibo name.
func (u *ui) suggestedFilename() string {
	u.Lock()
	name := u.lastName
	u.Unlock()

	if name == "" {
		return ""
	}
	return strings.ReplaceAll(name, "/", "-") + ".bin"
}

// draw draws the entire user interface.
func (u *ui) draw(animate bool) {
	width, _ := u.s.Size()

	x := width/2 - logoWidth()/2
	drawLogo(u.s, x, 0, animate)
	nextX := -1
	nextY := 0
	for _, e := range u.elements {
		// nextX+1 to have a vertical margin of one char.
		nextX, nextY = e.draw(animate, nextX+1, nextY)
	}
}

// show makes all ui content visible on the display.
func (u *ui) show() {
	u.s.Show()
}

// sync redraws the full screen on resize. It first waits for running box animations to end:
// clearing the screen and then blocking on an animating box would leave the boxes without
// borders until the animation finishes.
func (u *ui) sync() {
	for _, b := range []*box{u.infoBox, u.usageBox, u.imageBox.box, u.logBox} {
		b.Lock()
		b.Unlock() //lint:ignore SA2001 only used as an animation barrier
	}

	u.s.Clear()
	u.draw(false)
	u.s.Sync() // Is this still needed after the two preceding lines?
}

// pollEvent waits for events to arrive in the screen.
func (u *ui) pollEvent() tcell.Event {
	return u.s.PollEvent()
}

// destroy destroys the UI cleanly.
func (u *ui) destroy() {
	u.s.Fini()
}

// handleElementKey will block waiting for input for the active box.
func (u *ui) handleElementKey(r rune) {
	for _, b := range u.elements {
		if b.hasKey(r) {
			u.interact(b)
			return
		}
	}
}

// interact activates the given element and blocks handling input for it until it closes itself
// or is closed with ESC.
func (u *ui) interact(b element) {
	u.interactFor(b, u.amiibo())
}

// interactFor is interact operating on an explicitly given amiibo, so a modal can be driven for
// an amiibo that is no longer the active one. It returns true when the element completed and
// closed itself, false when it was aborted with ESC.
func (u *ui) interactFor(b element, am *amb) bool {
	deactivate := func(b element) {
		u.logBox.content <- encodeStringCell("Deactivating '" + b.name() + "' box")
		b.deactivate()
		if me, ok := b.(interface{ modalPart() *modal }); ok && me.modalPart().coverStale() {
			// The view changed while the modal was open: redraw so no modal remnants are left.
			u.queueRedraw()
		}
	}

	u.logBox.content <- encodeStringCell("Activating '" + b.name() + "' box...")
	done := b.activate(am)
	if done == nil {
		return false
	}
	modalOpen.Add(1)
	defer modalOpen.Add(-1)
	if me, ok := b.(interface{ modalPart() *modal }); ok {
		u.setActiveModal(me.modalPart())
		defer u.setActiveModal(nil)
	}
	u.logBox.content <- encodeStringCell("...'" + b.name() + "' box active; ESC to deactivate")
	for {
		select {
		case <-done:
			deactivate(b)
			return true
		default:
			ev := u.pollEvent()
			switch e := ev.(type) {
			case *tcell.EventMouse:
				// Translate the mouse wheel to arrow keys for the active element.
				switch e.Buttons() {
				case tcell.WheelUp:
					b.handleKey(tcell.NewEventKey(tcell.KeyUp, 0, tcell.ModNone))
				case tcell.WheelDown:
					b.handleKey(tcell.NewEventKey(tcell.KeyDown, 0, tcell.ModNone))
				}
			case *tcell.EventKey:
				switch {
				// TODO: do we deal with CTRL+C here, or just leave that be?
				case e.Key() == tcell.KeyEscape:
					deactivate(b)
					return false
				default:
					b.handleKey(e)
				}
			}
		}
	}
}

// clearView clears the active amiibo, keeping it in memory as a backup restorable with the b
// action. The visual clearing is requested through the amiibo channel, so it runs on the
// listener goroutine strictly after any reveal animations still in flight: a token removed while
// its data is being revealed clears gracefully instead of the two animations fighting.
func (u *ui) clearView() {
	u.Lock()
	if u.amb != nil {
		u.backup = u.amb
	}
	u.amb = nil
	u.ambNfcId = nil
	u.Unlock()

	amiiboChan <- &amb{clear: true}
}

// runClearAnimations clears the boxes with the reverse of the reveal animations, running in
// parallel. It is executed on the amiibo listener goroutine, see clearView.
func (u *ui) runClearAnimations() {
	var wg sync.WaitGroup
	wg.Add(3)
	go func() { defer wg.Done(); u.infoBox.clearContentTyped() }()
	go func() { defer wg.Done(); u.usageBox.clearContentTyped() }()
	go func() { defer wg.Done(); u.imageBox.dissolveImage() }()
	wg.Wait()

	u.logBox.content <- encodeStringCell("Amiibo view cleared, b restores it from memory")
	// Leave a pristine screen: this also repairs any leftovers of the removal prompt.
	u.sync()
}

// setActiveModal tracks the currently open modal in a thread safe way.
func (u *ui) setActiveModal(m *modal) {
	u.Lock()
	u.active = m
	u.Unlock()
}

// activeModal returns the currently open modal, nil when none is open.
func (u *ui) activeModal() *modal {
	u.Lock()
	defer u.Unlock()
	return u.active
}

// redrawActive redraws the currently open modal on top of whatever was just drawn beneath it,
// marking its screen snapshot stale so closing it triggers a full redraw instead of restoring
// outdated content.
func (u *ui) redrawActive() {
	m := u.activeModal()
	if m == nil {
		return
	}
	m.markCoverStale()
	m.draw(false, 0, 0)
	u.show()
}

// queueRedraw requests a full screen redraw through the amiibo channel, so it runs on the
// listener goroutine strictly after any reveal animation still in flight instead of wiping the
// screen underneath one. The send happens on a separate goroutine to not block the caller.
func (u *ui) queueRedraw() {
	go func() { amiiboChan <- &amb{redraw: true} }()
}

// tokenPresent returns whether a token is currently on the NFC portal in a thread safe way.
func (u *ui) tokenPresent() bool {
	u.Lock()
	defer u.Unlock()
	return u.tokenOn
}

// setTokenBadge updates the token presence state and its indicator on the logs box border.
func (u *ui) setTokenBadge(present bool) {
	u.Lock()
	u.tokenOn = present
	u.Unlock()
	if present {
		u.logBox.setBadge("═‡ ● token on portal ‡═", tcell.StyleDefault.Background(backColour).Foreground(tcell.ColorGreen).Attributes(tcell.AttrBold))
		return
	}
	u.logBox.setBadge("═‡ ○ no token ‡═", tcell.StyleDefault.Background(backColour).Foreground(fontColour).Attributes(tcell.AttrDim))
}

// restoreBackup reloads the last cleared amiibo as the active one.
func (u *ui) restoreBackup() {
	u.Lock()
	bk := u.backup
	u.Unlock()

	if bk == nil {
		u.logBox.content <- encodeStringCell("No amiibo in the backup memory")
		return
	}
	u.logBox.content <- encodeStringCell("Restoring amiibo from the backup memory")
	amiiboChan <- bk
}

// handleTokenRemoved shows the token removal prompt and clears the amiibo view unless the user
// chooses to save first, in which case the save modal is chained before clearing. Without an
// answer within removalTimeout the view is cleared anyway.
func (u *ui) handleTokenRemoved() {
	am := u.amiibo()
	if !conf.ui.clearOnRemove || am == nil || am.a == nil {
		return
	}
	if u.tokenPresent() {
		// The token was placed back (or replaced) before the prompt could open: nothing to
		// clear, the view already shows a present token.
		return
	}

	if u.removal.activate(am) == nil {
		u.clearView()
		return
	}
	modalOpen.Add(1)
	defer modalOpen.Add(-1)
	u.setActiveModal(u.removal.modal)
	defer u.setActiveModal(nil)

	// Drive the countdown and border flash of the prompt and post the timeout when the time is
	// up. The ticker runs at half second intervals to get a visible flash.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		deadline := time.Now().Add(removalTimeout)
		t := time.NewTicker(500 * time.Millisecond)
		defer t.Stop()
		for {
			select {
			case <-stop:
				return
			case now := <-t.C:
				rem := deadline.Sub(now)
				if rem <= 0 {
					u.s.PostEvent(newEventTokenRemoved(true))
					return
				}
				u.s.PostEvent(newEventRemovalTick(rem))
			}
		}
	}()

	// finish closes the prompt and, when asked to and the view still shows the removed token's
	// amiibo, clears the view. A new token may have replaced the view while the prompt was open;
	// in that case the new view is kept. The full redraw repairs whatever the background amiibo
	// listener may have drawn underneath the prompt in the meantime.
	finish := func(clear bool) {
		u.removal.deactivate()
		if u.amiibo() != am {
			u.logBox.content <- encodeStringCell("New token placed, keeping amiibo view")
			clear = false
		} else if clear {
			u.clearView()
		}
		if !clear {
			// Repair whatever the listener may have drawn underneath the prompt. Queued behind
			// in flight reveals; the clear path repaints by itself when its animations end.
			u.queueRedraw()
		}
	}

	for {
		ev := u.pollEvent()
		switch e := ev.(type) {
		case *eventRemovalTick:
			if u.amiibo() != am {
				// A new token was placed on the portal: the prompt is now moot.
				finish(false)
				return
			}
			u.removal.tick(e.remaining)
		case *eventTokenRemoved:
			if e.timedOut {
				u.logBox.content <- encodeStringCell("No answer, clearing amiibo view")
				finish(true)
				return
			}
		case *tcell.EventKey:
			switch {
			case e.Key() == tcell.KeyEscape || e.Rune() == 'c' || e.Rune() == 'C':
				finish(true)
				return
			case e.Rune() == 'k' || e.Rune() == 'K':
				u.logBox.content <- encodeStringCell("Keeping amiibo loaded")
				finish(false)
				return
			case e.Rune() == 's' || e.Rune() == 'S':
				u.removal.deactivate()
				// Save the amiibo the prompt was shown for, even when a new token has replaced
				// the view in the meantime.
				saved := u.interactFor(u.save, am)
				switch {
				case u.amiibo() != am:
					u.logBox.content <- encodeStringCell("New token placed, keeping amiibo view")
					u.queueRedraw()
				case saved:
					u.clearView()
				default:
					// The save was aborted: keep the view so the data is not lost.
					u.logBox.content <- encodeStringCell("Save aborted, keeping amiibo view")
					u.queueRedraw()
				}
				return
			}
		}
	}
}

// setAmiibo sets the active amiibo in a thread safe way.
func (u *ui) setAmiibo(a *amb) {
	u.Lock()
	u.amb = a
	if u.amb.nfc {
		u.ambNfcId = make([]byte, 8)
		copy(u.ambNfcId, u.amb.a.ModelInfo().ID())
	}
	u.Unlock()
}

// amiibo sets the active amiibo in a thread safe way.
func (u *ui) amiibo() *amb {
	u.Lock()
	a := u.amb
	u.Unlock()

	return a
}

// resetAmbNfcId will clear the ambNfcId field when a token is removed from the portal.
func (u *ui) resetAmbNfcId() {
	u.Lock()
	u.ambNfcId = nil
	u.Unlock()
}

// newUi create a new ui structure.
func newUi(invertImage bool) *ui {
	// Ordered in two workflow groups: dumps and viewing first, editing and options second, so a
	// two column layout shows them side by side.
	actionsContent := []string{
		"l: ", "load dump from disk",
		"s: ", "save dump to disk",
		"w: ", "write amiibo data to token",
		"d: ", "decrypt amiibo dump",
		"h: ", "hex view of (decrypted) dump",
		"i: ", "invert image view",
		"b: ", "restore last cleared amiibo",
		"n: ", "set amiibo nickname",
		"e: ", "edit gameplay (app) data",
		"f: ", "edit SSBU figure player",
		"r: ", "reset gameplay data",
		"t: ", "clear view on token removal",
		"ESC: ", "double press to quit",
	}

	s, _ := initScreen()
	info := newBox(s, boxOpts{title: "info", xPos: 1, yPos: logoHeight() + 1, width: 16, height: 70, typewriter: true})
	image := newImageBox(s, boxOpts{title: "image", xPos: -1, yPos: -1, width: 36, height: 70, bgColor: tcell.ColorBlack}, invertImage)
	usage := newBox(s, boxOpts{title: "usage", key: 'u', xPos: -1, yPos: -1, width: 46, height: 70, scroll: true, typewriter: true})
	// TODO: fix scrolling for boxes with the tail option!
	logs := newBox(s, boxOpts{title: "logs", stripLeadingSpace: true, xPos: -1, yPos: -1, width: 52, height: 20, tail: true, history: true})
	actions := newActionsBox(s, boxOpts{title: "actions", xPos: -1, yPos: -1, width: 46, height: 20}, actionsContent)

	u := &ui{
		s:        s,
		infoBox:  info,
		imageBox: image,
		usageBox: usage,
		logBox:   logs,
		write:    make(chan []byte),
	}

	// TODO: prevent overwriting modals when they're active (like reading a new amiibo while the dump modal is open)
	save := newFilenameModal(s, boxOpts{title: "save dump", key: 's', xPos: -1, yPos: -1, width: 30, height: 10, minHeight: 6, minWidth: 84, needAmiibo: true}, logs.content, saveDump)
	load := newFileBrowserModal(s, boxOpts{title: "load dump", key: 'l', xPos: -1, yPos: -1, width: 70, height: 70, minHeight: 24, minWidth: 84}, logs.content, loadDump)
	// TODO: it would be cool to highlight the different data blocks in the hex dump (like ID, save data, ...)
	hex := newTextModal(s, boxOpts{title: "view dump as hex", key: 'h', xPos: -1, yPos: -1, width: 84, height: 36, typ: boxTypeCharacter, needAmiibo: true, scroll: true}, logs.content)
	write := newOptionsModal(
		s,
		boxOpts{title: "write amiibo data to token", key: 'w', xPos: -1, yPos: -1, width: 80, height: 9, typ: boxTypeCharacter, needAmiibo: true},
		logs.content,
		[]mopts{{'f', "write full amiibo to token", 0}, {'u', "only write userdata to token (aka 'restore backup')", 1}},
		prepData,
		u.write,
	)

	nick := newTextInputModal(
		s,
		boxOpts{title: "set nickname", key: 'n', xPos: -1, yPos: -1, width: 30, height: 10, minHeight: 6, minWidth: 84, needAmiibo: true},
		"Enter new nickname (max 10 chars) followed by enter, ESC to abort:",
		logs.content,
		setNickname,
	)
	edit := newHexEditModal(s, boxOpts{title: "edit gameplay data", key: 'e', xPos: -1, yPos: -1, width: 78, height: 20, typ: boxTypeCharacter, needAmiibo: true}, logs.content, applyAppData)
	fp := newFPEditModal(s, boxOpts{title: "edit SSBU figure player", key: 'f', xPos: -1, yPos: -1, width: 80, height: 32, typ: boxTypeCharacter, needAmiibo: true}, logs.content, applyFPEdit)
	reset := newOptionsModal(
		s,
		boxOpts{title: "reset gameplay data", key: 'r', xPos: -1, yPos: -1, width: 80, height: 9, typ: boxTypeCharacter, needAmiibo: true},
		logs.content,
		[]mopts{{'w', "wipe all gameplay data so games see this amiibo as brand new", 0}},
		resetAppData,
		u.write,
	)

	u.elements = []element{info, image, usage, logs, actions, save, load, write, hex, nick, edit, fp, reset}
	u.save = save
	save.suggest = u.suggestedFilename
	u.removal = newRemovalPrompt(s, logs.content)
	u.actions = actions

	return u
}

// tui is the main terminal user interface loop. It sets up a tcell.Screen, draws the UI and
// handles UI related events.
func tui(conf *config) {
	u := newUi(conf.ui.invertImage)
	u.draw(true)

	var esc time.Time

	t := time.Now()

	amiiboChan = make(chan *amb)

	// Connect to the portal when the UI is visible, so it can display the client logs etc.
	ptl := newPortal(u.logBox.content, amiiboChan)
	go ptl.listen(conf)

	// Re-init loop for disconnect.
	go func() {
		for {
			select {
			case <-ptl.evt:
				u.setTokenBadge(false)
				ptl.log <- encodeStringCell("Reinitializing NFC portal")
				go ptl.listen(conf)
			case <-conf.quit:
				return
			}
		}
	}()

	// Listen for amiibo dumps.
	go func() {
		for {
			select {
			case am := <-amiiboChan:
				if am.redraw {
					u.sync()
					break
				}
				if am.clear {
					u.runClearAnimations()
					break
				}
				if am.nfc && am.a == nil {
					// An amb struct with nfc set to true and a nil amiibo signals a token removal.
					u.setTokenBadge(false)
					u.resetAmbNfcId()
					if conf.ui.clearOnRemove {
						// Let the main event loop show the removal prompt.
						u.s.PostEvent(newEventTokenRemoved(false))
					}
					break
				}
				if am.nfc {
					u.setTokenBadge(true)
				}
				u.setAmiibo(am)
				u.setLastName(showAmiiboInfo(am, u.logBox.content, u.infoBox.content, u.usageBox.content, u.imageBox, conf.amiiboApiBaseUrl))
				u.draw(false)
				// When a modal is open, the new amiibo just drew behind (animations are
				// suppressed): put the modal back on top.
				u.redrawActive()
			case data := <-u.write:
				writeToken(data, u.ambNfcId, ptl, u.logBox.content)
			case <-conf.quit:
				return
			}
		}
	}()

	u.show()
	u.setTokenBadge(false)

	if conf.retailKey == nil {
		u.logBox.content <- encodeStringCellWarning("No retail key loaded: cannot decrypt nor detect decrypted amiibo!")
	} else {
		u.logBox.content <- encodeStringCell("Retail key loaded: amiitool and crypto support available.")
	}

	if conf.expertMode {
		u.logBox.content <- encodeStringCellWarning("WARNING: expert mode activated, defunct amiibo may be written!")
	}

	for {
		ev := u.pollEvent()
		switch e := ev.(type) {
		case *tcell.EventResize:
			// This is a workaround for a screen flicker that happens immediately after the first screen draw. It seems
			// the resize event is always triggered after initial rendering?
			if time.Since(t) > 500*time.Millisecond {
				u.sync()
			}
		case *eventTokenRemoved:
			// A timed out event can only be a stray of an already closed prompt: the live one
			// consumes its own timeout.
			if !e.timedOut {
				u.handleTokenRemoved()
			}
		case *tcell.EventMouse:
			// The mouse wheel scrolls the usage box, the only box that regularly overflows.
			switch e.Buttons() {
			case tcell.WheelUp:
				u.usageBox.scrollTo(-3)
			case tcell.WheelDown:
				u.usageBox.scrollTo(3)
			}
		case *tcell.EventKey:
			switch {
			case e.Key() == tcell.KeyEscape || e.Key() == tcell.KeyCtrlC:
				if e.Key() == tcell.KeyCtrlC || !esc.IsZero() && time.Since(esc) <= 500*time.Millisecond {
					u.destroy()
					close(conf.quit)
					return
				}
				esc = time.Now()
				u.logBox.content <- encodeStringCell("Double press ESC to quit!")
			case e.Key() == tcell.KeyCtrlL:
				u.sync()
			case e.Rune() == 'D' || e.Rune() == 'd':
				if dec := decrypt(u.amiibo(), u.logBox.content); dec != nil {
					u.setAmiibo(dec)
				}
			case e.Rune() == 'I' || e.Rune() == 'i':
				u.logBox.content <- encodeStringCell("Toggle image invert")
				u.imageBox.invertImage()
			case e.Rune() == 'B' || e.Rune() == 'b':
				u.restoreBackup()
			case e.Key() == tcell.KeyLeft:
				u.actions.flip(-1)
			case e.Key() == tcell.KeyRight:
				u.actions.flip(1)
			// The vertical navigation keys scroll the usage box directly, no need to activate
			// it with its shortcut key first.
			case e.Key() == tcell.KeyUp:
				u.usageBox.scrollTo(-1)
			case e.Key() == tcell.KeyDown:
				u.usageBox.scrollTo(1)
			case e.Key() == tcell.KeyPgUp:
				u.usageBox.scrollTo(-u.usageBox.pageSize())
			case e.Key() == tcell.KeyPgDn:
				u.usageBox.scrollTo(u.usageBox.pageSize())
			case e.Rune() == 'T' || e.Rune() == 't':
				conf.ui.clearOnRemove = !conf.ui.clearOnRemove
				state := "enabled"
				if !conf.ui.clearOnRemove {
					state = "disabled"
				}
				u.logBox.content <- encodeStringCell("Clear view on token removal " + state)
				// Refresh the actions box so the toggle checkbox reflects the new state.
				u.actions.flip(0)
			default:
				u.handleElementKey(e.Rune())
			}
		}
	}
}
