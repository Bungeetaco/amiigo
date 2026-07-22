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
// animation.
func (i *imageBox) setImage(b image.Image) {
	i.img = b
	i.matrixReveal()
	i.drawImage()
}

// charMatrix converts the active image to a character matrix fitting the viewport of the box.
// It returns the matrix and the viewport width.
func (i *imageBox) charMatrix() ([][]ascii.CharPixel, int) {
	viewportWidth := i.width() - 4   // 4 = left and right borders + left and right margin
	viewportHeight := i.height() - 2 // 2 = only top and bottom borders

	// We calculate the new width according to the aspect ratio of the image, but since we are dealing with vertically
	// rectangular ASCII chars, we multiply the new width by a factor of two to get a somewhat square 'pixel' again.
	i.iOpts.FixedWidth = 2 * viewportHeight * i.img.Bounds().Max.X / i.img.Bounds().Max.Y
	i.iOpts.FixedHeight = viewportHeight

	// If the new calculated with turns out to be bigger than our viewport with, we'll adjust height based on the
	// viewport width.
	if i.iOpts.FixedWidth > viewportWidth {
		i.iOpts.FixedWidth = viewportWidth
		i.iOpts.FixedHeight = viewportWidth * i.img.Bounds().Max.X / i.img.Bounds().Max.Y
	}

	return i.conv.Image2CharPixelMatrix(i.img, &i.iOpts), viewportWidth
}

// matrixReveal draws the active image with a matrix style animation: a wave of random green
// glyphs rains down the box and resolves into the final image behind it.
func (i *imageBox) matrixReveal() {
	if i.img == nil {
		return
	}

	lines, viewportWidth := i.charMatrix()
	if len(lines) == 0 {
		return
	}

	marginLeft, _, marginTop, marginBottom := i.bounds()
	glyphs := []rune("abcdefghijklmnopqrstuvwxyz0123456789#$+*=%")
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
					// Resolved: the final pixel.
					style := tcell.StyleDefault.Foreground(tcell.NewRGBColor(int32(p.R), int32(p.G), int32(p.B))).Background(i.opts.bgColor).Attributes(i.attrs)
					i.s.SetContent(x, y, rune(p.Char), nil, style)
				case r == t:
					// The bright head of the wave.
					i.s.SetContent(x, y, glyphs[rand.Intn(len(glyphs))], nil, head)
				default:
					// The green trail.
					i.s.SetContent(x, y, glyphs[rand.Intn(len(glyphs))], nil, rain)
				}
			}
		}
		i.s.Show()
		time.Sleep(35 * time.Millisecond)
	}
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
		// Add end of line when the viewport width is bigger than the image width.
		if viewportWidth > len(l) {
			buf = append(buf, encodeStringCell("\n")...)
		}
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
