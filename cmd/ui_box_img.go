package main

import (
	"image"
	"math/rand"
	"time"

	"github.com/gdamore/tcell/v2"
	"github.com/qeesung/image2ascii/ascii"
	"github.com/qeesung/image2ascii/convert"
)

type imageBox struct {
	*box
	conv  *convert.ImageConverter
	img   image.Image
	iOpts convert.Options
	attrs tcell.AttrMask
}

// newImageBox creates a new imageBox struct ready for display on screen by calling box.draw().
// newImageBox also launches a single go routine to update the box contents as it comes in.
// If the given with and/or height in combination with boxTypeCharacter is smaller than the
// minWidth or minHeight, they will be ignored and set to the minimal values.
func newImageBox(s tcell.Screen, opts boxOpts, invert bool) *imageBox {
	attrs := tcell.AttrNone
	if invert {
		attrs = tcell.AttrReverse
	}

	i := &imageBox{
		box:   newBox(s, opts),
		conv:  convert.NewImageConverter(),
		iOpts: convert.DefaultOptions,
		attrs: attrs,
	}

	// Make sure the image is re-rendered on screen resize.
	i.redraw = func() {
		i.drawImage()
	}

	return i
}

// setImage will set the image on the box and update the box, revealing it with a matrix style
// animation. The box is blanked first so no cell of a previous, differently sized image lingers
// next to or below the new one.
func (i *imageBox) setImage(b image.Image) {
	i.clearContent()
	i.img = b
	// No reveal animation while a modal is open: the image draws behind the modal.
	if modalOpen.Load() == 0 {
		i.matrixReveal()
	}
	i.drawImage()
}

// charMatrix converts the active image to a character matrix fitting the viewport of the box.
// It returns the matrix and the viewport width.
func (i *imageBox) charMatrix() ([][]ascii.CharPixel, int) {
	// One less than the drawable width: a row filling the full drawable width would not get an
	// explicit newline and wrap by one character, shearing the whole image diagonally.
	viewportWidth := i.width() - 5
	viewportHeight := i.height() - 2 // 2 = only top and bottom borders

	// We calculate the new width according to the aspect ratio of the image, but since we are dealing with vertically
	// rectangular ASCII chars, we multiply the new width by a factor of two to get a somewhat square 'pixel' again.
	i.iOpts.FixedWidth = 2 * viewportHeight * i.img.Bounds().Max.X / i.img.Bounds().Max.Y
	i.iOpts.FixedHeight = viewportHeight

	// If the new calculated width turns out to be bigger than our viewport width, size by the
	// viewport width instead, dividing the height by the same factor of two.
	if i.iOpts.FixedWidth > viewportWidth {
		i.iOpts.FixedWidth = viewportWidth
		i.iOpts.FixedHeight = viewportWidth * i.img.Bounds().Max.Y / i.img.Bounds().Max.X / 2
	}

	return i.conv.Image2CharPixelMatrix(i.img, &i.iOpts), viewportWidth
}

// matrixGlyphs are the characters the matrix rain animation is made of.
var matrixGlyphs = []rune("abcdefghijklmnopqrstuvwxyz0123456789#$+*=%")

// matrixWave runs a matrix rain wave down the char matrix: a bright head of random green glyphs
// followed by a green trail, with the cells behind the wave rendered by the given settle
// function.
func (i *imageBox) matrixWave(lines [][]ascii.CharPixel, viewportWidth int, settle func(x, y int, p ascii.CharPixel)) {
	marginLeft, _, marginTop, marginBottom := i.bounds()
	head := tcell.StyleDefault.Foreground(tcell.NewRGBColor(180, 255, 180)).Background(i.opts.bgColor).Attributes(tcell.AttrBold)
	rain := tcell.StyleDefault.Foreground(tcell.NewRGBColor(0, 190, 60)).Background(i.opts.bgColor)

	const trail = 3
	for t := 0; t < len(lines)+trail+1; t++ {
		for r := 0; r < len(lines) && r <= t; r++ {
			y := marginTop + r
			if y > marginBottom {
				break
			}
			row := lines[r]
			pad := (viewportWidth - len(row)) / 2

			for c, p := range row {
				x := marginLeft + pad + c
				switch {
				case r <= t-trail:
					settle(x, y, p)
				case r == t:
					i.s.SetContent(x, y, matrixGlyphs[rand.Intn(len(matrixGlyphs))], nil, head)
				default:
					i.s.SetContent(x, y, matrixGlyphs[rand.Intn(len(matrixGlyphs))], nil, rain)
				}
			}
		}
		i.s.Show()
		time.Sleep(35 * time.Millisecond)
	}
}

// matrixReveal draws the active image with a matrix style animation: the wave resolves into the
// final image behind it.
func (i *imageBox) matrixReveal() {
	if i.img == nil {
		return
	}
	lines, viewportWidth := i.charMatrix()
	if len(lines) == 0 {
		return
	}

	i.matrixWave(lines, viewportWidth, func(x, y int, p ascii.CharPixel) {
		style := tcell.StyleDefault.Foreground(tcell.NewRGBColor(int32(p.R), int32(p.G), int32(p.B))).Background(i.opts.bgColor).Attributes(i.attrs)
		i.s.SetContent(x, y, rune(p.Char), nil, style)
	})
}

// dissolveImage clears the active image with the reverse matrix style animation: the wave eats
// the image and leaves a blank box behind it.
func (i *imageBox) dissolveImage() {
	if i.img == nil {
		i.clearImage()
		return
	}
	lines, viewportWidth := i.charMatrix()
	if len(lines) == 0 {
		i.clearImage()
		return
	}

	blank := tcell.StyleDefault.Background(i.opts.bgColor)
	i.matrixWave(lines, viewportWidth, func(x, y int, _ ascii.CharPixel) {
		i.s.SetContent(x, y, 0, nil, blank)
	})
	i.clearImage()
}

// drawImage will convert the active image to a printable ASCII string and send it to the content
// channel for display.
func (i *imageBox) drawImage() {
	if i.img == nil {
		return
	}

	// TODO: image rendering still has bugs, e.g.: try a Kirby amiibo in different terminal sizes.

	lines, viewportWidth := i.charMatrix()

	var buf []byte
	for _, l := range lines {
		// Add padding to center image (-2 for the borders).
		for j := 0; j < (viewportWidth-len(l))/2; j++ {
			buf = append(buf, encodeImageCell(ascii.CharPixel{Char: ' '}, i.attrs)...)
		}
		// Render image line.
		for _, p := range l {
			// TODO: add vertical padding
			buf = append(buf, encodeImageCell(p, i.attrs)...)
		}
		// Every row ends in an explicit newline: relying on the implicit wrap for rows that fill
		// the whole drawable width shifts each subsequent row by one character, shearing the
		// image diagonally.
		buf = append(buf, encodeStringCell("\n")...)
	}

	i.content <- buf
}

// clearImage removes the active image from the box and blanks its content area.
func (i *imageBox) clearImage() {
	i.img = nil
	i.clearContent()
}

// invertImage inverts the display of the active image.
func (i *imageBox) invertImage() {
	switch i.attrs {
	case tcell.AttrNone:
		i.attrs = tcell.AttrReverse
	case tcell.AttrReverse:
		i.attrs = tcell.AttrNone
	}

	i.drawImage()
}
