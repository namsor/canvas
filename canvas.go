package canvas

import (
	"fmt"
	"image"
	"image/color"
	"image/draw"
	"io"
	"math"

	"golang.org/x/image/vector"
)

const MmPerPt = 0.3527777777777778
const PtPerMm = 2.8346456692913384
const MmPerInch = 25.4
const InchPerMm = 1 / 25.4

type C struct {
	w, h   float64
	layers []layer
	fonts  map[*Font]bool
	// TODO: add transformation matrix / viewport
	drawState
}

func New(w, h float64) *C {
	return &C{w, h, []layer{}, map[*Font]bool{}, defaultDrawState}
}

func (c *C) SetFillColor(color color.RGBA) {
	c.fillColor = color
}

func (c *C) SetStrokeColor(color color.RGBA) {
	c.strokeColor = color
}

func (c *C) SetStrokeWidth(width float64) {
	c.strokeWidth = width
}

func (c *C) SetStrokeCapper(capper Capper) {
	c.strokeCapper = capper
}

func (c *C) SetStrokeJoiner(joiner Joiner) {
	c.strokeJoiner = joiner
}

func (c *C) SetDashes(dashOffset float64, dashes ...float64) {
	c.dashOffset = dashOffset
	c.dashes = dashes
}

func (c *C) DrawPath(x, y float64, path *Path) {
	if !path.Empty() {
		path = path.Copy().Translate(x, y)
		c.layers = append(c.layers, pathLayer{path, c.drawState})
	}
}

func (c *C) DrawText(x, y float64, text *Text) {
	for font := range text.fonts {
		c.fonts[font] = true
	}
	// TODO: skip if empty
	c.layers = append(c.layers, textLayer{text, x, y, 0.0})
}

// TODO: add DrawImage(x,y,image.RGBA)

func (c *C) WriteSVG(w io.Writer) {
	fmt.Fprintf(w, `<svg xmlns="http://www.w3.org/2000/svg" version="1.1" shape-rendering="geometricPrecision" width="%g" height="%g" viewBox="0 0 %g %g">`, c.w, c.h, c.w, c.h)
	if len(c.fonts) > 0 {
		fmt.Fprintf(w, "<defs><style>")
		for f := range c.fonts {
			fmt.Fprintf(w, "\n@font-face{font-family:'%s';src:url('%s');}", f.name, f.ToDataURI())
		}
		fmt.Fprintf(w, "\n</style></defs>")
	}
	for _, l := range c.layers {
		l.WriteSVG(w, c.h)
	}
	fmt.Fprintf(w, "</svg>")
}

func (c *C) WritePDF(w io.Writer) error {
	pdf := NewPDFWriter(w)
	pdfpage := pdf.NewPage(c.w, c.h)
	for _, l := range c.layers {
		l.WritePDF(pdfpage)
	}
	return pdf.Close()
}

// WriteEPS writes out the image in the EPS file format.
// Be aware that EPS does not support transparency of colors.
func (c *C) WriteEPS(w io.Writer) {
	eps := NewEPSWriter(w, c.w, c.h)
	for _, l := range c.layers {
		eps.Write([]byte("\n"))
		l.WriteEPS(eps)
	}
}

func (c *C) WriteImage(dpi float64) *image.RGBA {
	dpm := dpi * InchPerMm
	img := image.NewRGBA(image.Rect(0.0, 0.0, int(c.w*dpm+0.5), int(c.h*dpm+0.5)))
	draw.Draw(img, img.Bounds(), image.NewUniform(White), image.Point{}, draw.Src)
	for _, l := range c.layers {
		l.WriteImage(img, dpm, c.w, c.h)
	}
	return img
}

////////////////////////////////////////////////////////////////

type layer interface {
	WriteSVG(io.Writer, float64)
	WritePDF(*PDFPageWriter)
	WriteEPS(*EPSWriter)
	WriteImage(*image.RGBA, float64, float64, float64)
}

type drawState struct {
	fillColor, strokeColor color.RGBA
	strokeWidth            float64
	strokeCapper           Capper
	strokeJoiner           Joiner
	dashOffset             float64
	dashes                 []float64
}

var defaultDrawState = drawState{
	fillColor:    Black,
	strokeColor:  Transparent,
	strokeWidth:  1.0,
	strokeCapper: ButtCapper,
	strokeJoiner: MiterJoiner,
	dashOffset:   0.0,
	dashes:       []float64{},
}

type pathLayer struct {
	path *Path
	drawState
}

func (l pathLayer) WriteSVG(w io.Writer, h float64) {
	p := l.path.Copy().Scale(1.0, -1.0).Translate(0.0, h)
	w.Write([]byte(`<path d="`))
	w.Write([]byte(p.ToSVG()))
	// TODO: draw explicit stroke when miter has non-bevel fallback or arcs has a limit
	if l.strokeColor.A != 0 && 0.0 < l.strokeWidth {
		fmt.Fprintf(w, `" style="stroke:%s`, toCSSColor(l.strokeColor))
		if l.strokeWidth != 1.0 {
			fmt.Fprintf(w, ";stroke-width:%g", l.strokeWidth)
		}
		if _, ok := l.strokeCapper.(roundCapper); ok {
			fmt.Fprintf(w, ";stroke-linecap:round")
		} else if _, ok := l.strokeCapper.(squareCapper); ok {
			fmt.Fprintf(w, ";stroke-linecap:square")
		} else if _, ok := l.strokeCapper.(buttCapper); !ok {
			panic("SVG: line cap not support")
		}
		if _, ok := l.strokeJoiner.(bevelJoiner); ok {
			fmt.Fprintf(w, ";stroke-linejoin:bevel")
		} else if _, ok := l.strokeJoiner.(roundJoiner); ok {
			fmt.Fprintf(w, ";stroke-linejoin:round")
		} else if _, ok := l.strokeJoiner.(arcsJoiner); ok {
			fmt.Fprintf(w, ";stroke-linejoin:arcs")
		} else if miter, ok := l.strokeJoiner.(miterJoiner); ok && !math.IsNaN(miter.limit) {
			// without a miter limit it becomes a 'miter' linejoin, which is the default
			fmt.Fprintf(w, ";stroke-linejoin:miter-clip") // TODO: wrong, miter-clip only changes the resulting bevel not whether to clip at the limit or not, this happens for both
			if miter.limit != 4.0 {
				fmt.Fprintf(w, ";stroke-miterlimit:%g", miter.limit) // TODO: needs to be divided by 2?
			}
		} else if _, ok := l.strokeJoiner.(miterJoiner); !ok {
			panic("SVG: line join not support")
		}

		if 0 < len(l.dashes) {
			fmt.Fprintf(w, ";stroke-dasharray:%g", l.dashes[0])
			for _, dash := range l.dashes[1:] {
				fmt.Fprintf(w, " %g", dash)
			}
			if 0.0 < l.dashOffset {
				fmt.Fprintf(w, ";stroke-dashoffset:%g", l.dashOffset)
			}
		}
		if l.fillColor != Black {
			if l.fillColor.A != 0 {
				fmt.Fprintf(w, ";fill:%s", toCSSColor(l.fillColor))
			} else {
				fmt.Fprintf(w, ";fill:none")
			}
		}
		if FillRule == EvenOdd {
			fmt.Fprintf(w, ";fill-rule:evenodd")
		}
	} else if l.fillColor != Black {
		if l.fillColor.A != 0 {
			fmt.Fprintf(w, `" fill="%s`, toCSSColor(l.fillColor))
		} else {
			fmt.Fprintf(w, `" fill="none`)
		}
	}
	w.Write([]byte(`"/>`))
}

func (l pathLayer) WritePDF(w *PDFPageWriter) {
	fill := l.fillColor.A != 0
	stroke := l.strokeColor.A != 0 && 0.0 < l.strokeWidth
	if !fill && !stroke {
		return
	}

	closed := false
	data := l.path.ToPDF()
	if 1 < len(data) && data[len(data)-1] == 'h' {
		data = data[:len(data)-2]
		closed = true
	}

	differentAlpha := fill && stroke && l.fillColor.A != l.strokeColor.A

	// PDFs don't support the arcs joiner, miter joiner (not clipped), or miter-clip joiner with non-bevel fallback
	_, strokeUnsupported := l.strokeJoiner.(arcsJoiner)
	if miter, ok := l.strokeJoiner.(miterJoiner); ok {
		if math.IsNaN(miter.length) {
			strokeUnsupported = true
		} else if _, ok := miter.gapJoiner.(bevelJoiner); !ok {
			strokeUnsupported = true
		}
	}

	if differentAlpha || strokeUnsupported {
		// draw both paths separately
		w.SetFillColor(l.fillColor)
		w.Write([]byte(" "))
		w.Write([]byte(data))
		w.Write([]byte(" f"))
		if FillRule == EvenOdd {
			w.Write([]byte("*"))
		}

		if strokeUnsupported {
			// stroke settings unsupported by PDF, draw stroke explicitly
			strokePath := l.path.Copy()
			if 0 < len(l.dashes) {
				strokePath = strokePath.Dash(l.dashOffset, l.dashes...)
			}
			strokePath = strokePath.Stroke(l.strokeWidth, l.strokeCapper, l.strokeJoiner)

			w.SetFillColor(l.strokeColor)
			w.Write([]byte(" "))
			w.Write([]byte(strokePath.ToPDF()))
			w.Write([]byte(" f"))
			if FillRule == EvenOdd {
				w.Write([]byte("*"))
			}
		} else {
			// setting alpha would otherwise interfere between fill and stroke
			w.SetStrokeColor(l.strokeColor)
			w.SetLineWidth(l.strokeWidth)
			w.SetLineCap(l.strokeCapper)
			w.SetLineJoin(l.strokeJoiner)
			w.SetDashes(l.dashOffset, l.dashes)
			w.Write([]byte(" "))
			w.Write([]byte(data))
			if closed {
				w.Write([]byte(" s"))
			} else {
				w.Write([]byte(" S"))
			}
			if FillRule == EvenOdd {
				w.Write([]byte("*"))
			}
		}
	} else {
		if fill {
			w.SetFillColor(l.fillColor)
		}
		if stroke {
			w.SetStrokeColor(l.strokeColor)
			w.SetLineWidth(l.strokeWidth)
			w.SetLineCap(l.strokeCapper)
			w.SetLineJoin(l.strokeJoiner)
			w.SetDashes(l.dashOffset, l.dashes)
		}

		w.Write([]byte(" "))
		w.Write([]byte(l.path.ToPDF()))
		if fill && stroke {
			if closed {
				w.Write([]byte(" b"))
			} else {
				w.Write([]byte(" B"))
			}
		} else if fill {
			w.Write([]byte(" f"))
		} else {
			if closed {
				w.Write([]byte(" s"))
			} else {
				w.Write([]byte(" S"))
			}
		}
		if fill && FillRule == EvenOdd {
			w.Write([]byte("*"))
		}
	}
}

func (l pathLayer) WriteEPS(w *EPSWriter) {
	// TODO: EPS test ellipse, rotations etc
	w.SetColor(l.fillColor)
	w.Write([]byte(" "))
	w.Write([]byte(l.path.ToPS()))
	w.Write([]byte(" fill"))
	// TODO: EPS add drawState support
}

func (l pathLayer) WriteImage(img *image.RGBA, dpm, w, h float64) {
	if l.fillColor.A != 0 {
		ras := vector.NewRasterizer(int(w*dpm+0.5), int(h*dpm+0.5))
		l.path.ToRasterizer(ras, dpm, w, h)
		size := ras.Size()
		ras.Draw(img, image.Rect(0, 0, size.X, size.Y), image.NewUniform(l.fillColor), image.Point{})
	}
	if l.strokeColor.A != 0 && 0.0 < l.strokeWidth {
		stroke := l.path.Copy()
		if 0 < len(l.dashes) {
			stroke = stroke.Dash(l.dashOffset, l.dashes...)
		}
		stroke = stroke.Stroke(l.strokeWidth, l.strokeCapper, l.strokeJoiner)

		ras := vector.NewRasterizer(int(w*dpm+0.5), int(h*dpm+0.5))
		stroke.ToRasterizer(ras, dpm, w, h)
		size := ras.Size()
		ras.Draw(img, image.Rect(0, 0, size.X, size.Y), image.NewUniform(l.strokeColor), image.Point{})
	}
}

type textLayer struct {
	*Text
	x, y, rot float64
}

func (l textLayer) WriteSVG(w io.Writer, h float64) {
	l.Text.WriteSVG(w, l.x, h-l.y, l.rot)
}

func (l textLayer) WritePDF(w *PDFPageWriter) {
	// TODO: PDF write text
	paths, colors := l.ToPaths()
	for i, path := range paths {
		path.Rotate(l.rot, 0.0, 0.0).Translate(l.x, l.y)
		state := defaultDrawState
		state.fillColor = colors[i]
		pathLayer{path, state}.WritePDF(w)
	}
}

func (l textLayer) WriteEPS(w *EPSWriter) {
	// TODO: EPS write text
	paths, colors := l.ToPaths()
	for i, path := range paths {
		path.Rotate(l.rot, 0.0, 0.0).Translate(l.x, l.y)
		state := defaultDrawState
		state.fillColor = colors[i]
		pathLayer{path, state}.WriteEPS(w)
	}
}

func (l textLayer) WriteImage(img *image.RGBA, dpm, w, h float64) {
	paths, colors := l.ToPaths()
	for i, path := range paths {
		path.Rotate(l.rot, 0.0, 0.0).Translate(l.x, l.y)
		state := defaultDrawState
		state.fillColor = colors[i]
		pathLayer{path, state}.WriteImage(img, dpm, w, h)
	}
}
