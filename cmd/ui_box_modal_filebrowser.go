package main

import (
	"encoding/hex"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/gdamore/tcell/v2"
	"github.com/malc0mn/amiigo/amiibo"
	"github.com/malc0mn/amiigo/apii"
	"github.com/qeesung/image2ascii/convert"
)

// fbEntry represents a single entry in the file browser listing.
type fbEntry struct {
	name string
	dir  bool
	size int64
}

// fbPreviewLine is a single rendered line in the preview pane.
type fbPreviewLine struct {
	label string
	value string
}

// fbPreview holds the cached preview data for a single dump file.
type fbPreview struct {
	lines  []fbPreviewLine
	imgURL string
}

// fileBrowserModal represents a modal that lets the user pick an amiibo dump from disk using a
// navigable directory listing with a preview pane showing the dump contents.
type fileBrowserModal struct {
	*modal
	dir     string
	entries []fbEntry
	sel     int
	offset  int
	submit  filenameSubmitHandler
	preview map[string]*fbPreview // Preview cache keyed by full file path.
	api     *apii.AmiiboAPI
	apiDown bool // Set when the info API is unreachable so we stop trying for this session.
	conv    *convert.ImageConverter
	imgs    map[string]image.Image // Image cache keyed by image url.
	imgFail map[string]bool        // Failed image fetches, so we don't retry on every keypress.
}

// newFileBrowserModal creates a new fileBrowserModal struct ready for use. The submit handler
// receives the full path of the selected file.
func newFileBrowserModal(s tcell.Screen, opts boxOpts, log chan<- []byte, submit filenameSubmitHandler) *fileBrowserModal {
	fb := &fileBrowserModal{
		submit:  submit,
		preview: make(map[string]*fbPreview),
		conv:    convert.NewImageConverter(),
		imgs:    make(map[string]image.Image),
		imgFail: make(map[string]bool),
	}
	fb.modal = newModal(s, opts, fb.handleInput, fb.drawModalContent, nil, log)

	return fb
}

// loadDir reads the current directory into fb.entries, directories first, then .bin files, both
// sorted case-insensitively. Dotfiles are hidden.
func (fb *fileBrowserModal) loadDir() {
	if fb.dir == "" {
		fb.dir, _ = os.Getwd()
	}

	fb.entries = nil
	if filepath.Dir(fb.dir) != fb.dir {
		fb.entries = append(fb.entries, fbEntry{name: "..", dir: true})
	}

	items, err := os.ReadDir(fb.dir)
	if err != nil {
		fb.log <- encodeStringCell("File browser: " + err.Error())
		return
	}

	var dirs, files []fbEntry
	for _, it := range items {
		name := it.Name()
		if strings.HasPrefix(name, ".") {
			continue
		}
		if it.IsDir() {
			dirs = append(dirs, fbEntry{name: name, dir: true})
			continue
		}
		if strings.EqualFold(filepath.Ext(name), ".bin") {
			e := fbEntry{name: name}
			if fi, err := it.Info(); err == nil {
				e.size = fi.Size()
			}
			files = append(files, e)
		}
	}

	less := func(s []fbEntry) func(i, j int) bool {
		return func(i, j int) bool { return strings.ToLower(s[i].name) < strings.ToLower(s[j].name) }
	}
	sort.Slice(dirs, less(dirs))
	sort.Slice(files, less(files))

	fb.entries = append(fb.entries, dirs...)
	fb.entries = append(fb.entries, files...)
}

// cd changes the current directory and resets the selection.
func (fb *fileBrowserModal) cd(name string) {
	fb.dir = filepath.Clean(filepath.Join(fb.dir, name))
	fb.sel = 0
	fb.offset = 0
	fb.loadDir()
	fb.redrawContent()
}

// geom returns the drawable content geometry: left and right columns, the path line row, the first
// and last list rows, the separator column and the first preview column.
func (fb *fileBrowserModal) geom() (left, right, pathY, listTop, bottom, sepX, prevX int) {
	x, y := fb.getXY()
	left = x + 2
	right = x + fb.width() - 3
	pathY = y + 1
	listTop = y + 3
	bottom = y + fb.height() - 2

	w := right - left + 1
	listW := w * 2 / 5
	if listW < 22 {
		listW = 22
	}
	sepX = left + listW
	prevX = sepX + 2

	return
}

// pageRows returns the number of visible rows in the file listing.
func (fb *fileBrowserModal) pageRows() int {
	_, _, _, listTop, bottom, _, _ := fb.geom()
	rows := bottom - listTop + 1
	if rows < 1 {
		rows = 1
	}
	return rows
}

// handleInput will handle keyboard input for the fileBrowserModal.
func (fb *fileBrowserModal) handleInput(e *tcell.EventKey) {
	switch {
	case e.Key() == tcell.KeyUp:
		fb.move(-1)
	case e.Key() == tcell.KeyDown:
		fb.move(1)
	case e.Key() == tcell.KeyPgUp:
		fb.move(-fb.pageRows())
	case e.Key() == tcell.KeyPgDn:
		fb.move(fb.pageRows())
	case e.Key() == tcell.KeyHome:
		fb.move(-len(fb.entries))
	case e.Key() == tcell.KeyEnd:
		fb.move(len(fb.entries))
	case e.Key() == tcell.KeyLeft || e.Key() == tcell.KeyBackspace || e.Key() == tcell.KeyBackspace2:
		fb.cd("..")
	case e.Key() == tcell.KeyRight:
		if ent, ok := fb.selected(); ok && ent.dir {
			fb.cd(ent.name)
		}
	case e.Key() == tcell.KeyEnter || e.Rune() == '\n':
		ent, ok := fb.selected()
		if !ok {
			return
		}
		if ent.dir {
			fb.cd(ent.name)
			return
		}
		if fb.submit(filepath.Join(fb.dir, ent.name), fb.amb, fb.log) {
			// Signal the modal is done.
			fb.end()
		}
	}
}

// selected returns the currently highlighted entry.
func (fb *fileBrowserModal) selected() (fbEntry, bool) {
	if fb.sel < 0 || fb.sel >= len(fb.entries) {
		return fbEntry{}, false
	}
	return fb.entries[fb.sel], true
}

// move shifts the selection by n entries, keeping selection and scroll offset within bounds.
func (fb *fileBrowserModal) move(n int) {
	fb.sel += n
	if fb.sel < 0 {
		fb.sel = 0
	}
	if fb.sel > len(fb.entries)-1 {
		fb.sel = len(fb.entries) - 1
	}
	fb.redrawContent()
}

// drawModalContent will handle displaying of the modal content.
func (fb *fileBrowserModal) drawModalContent(_, _ int) {
	if fb.entries == nil {
		fb.loadDir()
	}
	fb.redrawContent()
}

// redrawContent redraws the path line, the file listing and the preview pane.
func (fb *fileBrowserModal) redrawContent() {
	left, right, pathY, listTop, bottom, sepX, prevX := fb.geom()
	base := tcell.StyleDefault.Background(backColour).Foreground(fontColour)

	// Clear the content area.
	for y := pathY; y <= bottom; y++ {
		for x := left; x <= right; x++ {
			fb.s.SetContent(x, y, ' ', nil, base)
		}
	}

	// Path line with selection position indicator.
	pos := ""
	if len(fb.entries) > 0 {
		pos = fmt.Sprintf(" (%d/%d)", fb.sel+1, len(fb.entries))
	}
	fb.drawStr(left, pathY, truncLeft(fb.dir, right-left+1-len(pos))+pos, base.Attributes(tcell.AttrBold))

	// Separator.
	for y := listTop; y <= bottom; y++ {
		fb.s.SetContent(sepX, y, boxLineVertical, nil, base)
	}

	// File listing.
	rows := bottom - listTop + 1
	if fb.sel < fb.offset {
		fb.offset = fb.sel
	}
	if fb.sel > fb.offset+rows-1 {
		fb.offset = fb.sel - rows + 1
	}
	listW := sepX - left - 1
	if len(fb.entries) == 0 {
		fb.drawStr(left, listTop, "(no dumps found)", base.Attributes(tcell.AttrDim))
	}
	for i := 0; i < rows && fb.offset+i < len(fb.entries); i++ {
		ent := fb.entries[fb.offset+i]
		name := ent.name
		if ent.dir {
			name += "/"
		}
		style := base
		if fb.offset+i == fb.sel {
			style = style.Attributes(tcell.AttrReverse)
			// Draw the full selection bar first.
			fb.drawStr(left, listTop+i, strings.Repeat(" ", listW), style)
		}
		fb.drawStr(left, listTop+i, trunc(name, listW), style)
	}

	// Preview pane.
	fb.drawPreview(prevX, listTop, right, bottom, base)

	fb.s.Show()
}

// drawPreview renders the preview of the currently selected entry in the given screen region.
func (fb *fileBrowserModal) drawPreview(x, y, right, bottom int, base tcell.Style) {
	ent, ok := fb.selected()
	if !ok {
		return
	}

	w := right - x + 1
	if w < 10 {
		return
	}

	var p *fbPreview
	if ent.dir {
		p = &fbPreview{lines: []fbPreviewLine{{label: "Directory", value: ent.name}}}
	} else {
		p = fb.previewFor(filepath.Join(fb.dir, ent.name), ent.size)
	}

	labelStyle := base.Attributes(tcell.AttrBold | tcell.AttrDim)
	for i, l := range p.lines {
		if y+i > bottom {
			break
		}
		col := x
		if l.label != "" {
			fb.drawStr(col, y+i, trunc(l.label+":", w), labelStyle)
			col += len(l.label) + 2
		}
		if col <= right {
			fb.drawStr(col, y+i, trunc(l.value, right-col+1), base)
		}
	}

	if img := fb.previewImage(p.imgURL); img != nil {
		fb.drawPreviewImage(img, x, y+len(p.lines)+1, right, bottom)
	}
}

// previewImage fetches (and caches) the image for the given url. Returns nil when there is no url
// or the image cannot be fetched.
func (fb *fileBrowserModal) previewImage(url string) image.Image {
	if url == "" || fb.imgFail[url] {
		return nil
	}
	if img, ok := fb.imgs[url]; ok {
		return img
	}

	img, err := getImage(url)
	if err != nil {
		fb.imgFail[url] = true
		return nil
	}
	fb.imgs[url] = img

	return img
}

// drawPreviewImage renders the given image as coloured ASCII in the given screen region, scaled to
// fit and horizontally centered.
func (fb *fileBrowserModal) drawPreviewImage(img image.Image, x, y, right, bottom int) {
	w := right - x + 1
	h := bottom - y + 1
	iw := img.Bounds().Dx()
	ih := img.Bounds().Dy()
	if w < 4 || h < 4 || iw == 0 || ih == 0 {
		return
	}

	// Terminal cells are roughly twice as high as they are wide, so we multiply the width by two
	// to keep the aspect ratio.
	opts := convert.DefaultOptions
	opts.FixedWidth = 2 * h * iw / ih
	opts.FixedHeight = h
	if opts.FixedWidth > w {
		opts.FixedWidth = w
		opts.FixedHeight = w * ih / iw / 2
	}

	pad := (w - opts.FixedWidth) / 2
	for row, l := range fb.conv.Image2CharPixelMatrix(img, &opts) {
		if y+row > bottom {
			break
		}
		for col, p := range l {
			style := tcell.StyleDefault.Background(backColour).Foreground(tcell.NewRGBColor(int32(p.R), int32(p.G), int32(p.B)))
			fb.s.SetContent(x+pad+col, y+row, rune(p.Char), nil, style)
		}
	}
}

// previewFor builds (and caches) the preview for the given dump file.
func (fb *fileBrowserModal) previewFor(path string, size int64) *fbPreview {
	if p, ok := fb.preview[path]; ok {
		return p
	}

	p := &fbPreview{lines: []fbPreviewLine{
		{label: "File", value: filepath.Base(path)},
		{label: "Size", value: fmt.Sprintf("%d bytes", size)},
		{},
	}}
	fb.preview[path] = p

	data, err := os.ReadFile(path)
	if err != nil {
		p.lines = append(p.lines, fbPreviewLine{value: "Cannot read file:"}, fbPreviewLine{value: err.Error()})
		return p
	}

	var dump amiibo.Amiidump
	format := "amiibo"
	if t := isAmiiTool(data, conf.retailKey); t != nil {
		dump = t
		format = "amiitool"
	} else if a, err := amiibo.NewAmiibo(data, nil); err == nil {
		dump = a
	} else {
		p.lines = append(p.lines, fbPreviewLine{value: "Not a valid amiibo dump:"}, fbPreviewLine{value: err.Error()})
		return p
	}

	crypto := "unknown (no retail key)"
	if conf.retailKey != nil {
		crypto = "encrypted"
		if isAmiiboDecrypted(dump, conf.retailKey) {
			crypto = "decrypted"
		}
	}

	id := hex.EncodeToString(dump.ModelInfo().ID())
	p.lines = append(p.lines,
		fbPreviewLine{label: "Format", value: format},
		fbPreviewLine{label: "Crypto", value: crypto},
		fbPreviewLine{label: "ID", value: "0x" + id},
		fbPreviewLine{},
	)
	fb.apiPreview(id, p)

	return p
}

// apiPreview fetches character info for the given amiibo id from the info API and appends it to
// the given preview.
func (fb *fileBrowserModal) apiPreview(id string, p *fbPreview) {
	if fb.apiDown {
		p.lines = append(p.lines, fbPreviewLine{value: "(character info unavailable)"})
		return
	}
	if fb.api == nil {
		fb.api = apii.NewAmiiboAPI(newCachedHttpClient(), conf.amiiboApiBaseUrl)
	}

	ai, err := fb.api.GetAmiiboInfoById(id)
	if err != nil {
		if strings.Contains(err.Error(), "connection refused") || strings.Contains(err.Error(), "Timeout") {
			// Don't keep hammering an unreachable API on every keypress.
			fb.apiDown = true
		}
		p.lines = append(p.lines, fbPreviewLine{value: "No character info:"}, fbPreviewLine{value: err.Error()})
		return
	}

	p.lines = append(p.lines,
		fbPreviewLine{label: "Name", value: ai.Name},
		fbPreviewLine{label: "Character", value: ai.Character},
		fbPreviewLine{label: "Type", value: ai.Type},
		fbPreviewLine{label: "Amiibo Series", value: ai.AmiiboSeries},
		fbPreviewLine{label: "Game series", value: ai.GameSeries},
		fbPreviewLine{label: "Release NA", value: ai.Release["na"]},
	)
	p.imgURL = ai.Image
}

// drawStr draws a string on the given position with the given style.
func (fb *fileBrowserModal) drawStr(x, y int, s string, style tcell.Style) {
	for i, r := range []rune(s) {
		fb.s.SetContent(x+i, y, r, nil, style)
	}
}

// trunc shortens a string to the given width, marking the cut with an ellipsis at the end.
func trunc(s string, w int) string {
	r := []rune(s)
	if w <= 0 {
		return ""
	}
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return string(r[:w-1]) + "…"
}

// truncLeft shortens a string to the given width, marking the cut with an ellipsis at the start so
// the end of the string remains visible.
func truncLeft(s string, w int) string {
	r := []rune(s)
	if w <= 0 {
		return ""
	}
	if len(r) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	return "…" + string(r[len(r)-w+1:])
}
