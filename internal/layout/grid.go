// Package layout maps grid coordinates onto pixels.
//
// The geometry is carried over from v1's include/modules/widgets/widget_base.h:
// a 12x16 grid with 20px padding and a 5px gap. 16 rows rather than something
// rounder because the v1 layout wanted tighter vertical stacking than 12
// would give.
package layout

import "image"

// Defaults matching the v1 widget grid.
const (
	DefaultCols    = 12
	DefaultRows    = 16
	DefaultPadding = 20
	DefaultGap     = 5
)

// Span is a widget's size in grid cells.
type Span struct {
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

// Pos is a widget's placement: origin plus span.
type Pos struct {
	Col     int `json:"col"`
	Row     int `json:"row"`
	ColSpan int `json:"colSpan"`
	RowSpan int `json:"rowSpan"`
}

// Span returns just the size part of a Pos.
func (p Pos) Span() Span { return Span{Cols: p.ColSpan, Rows: p.RowSpan} }

// Grid divides a rectangle into evenly sized cells.
type Grid struct {
	Bounds  image.Rectangle
	Cols    int
	Rows    int
	PadX    int
	PadY    int
	GapX    int
	GapY    int
}

// New returns a Grid over bounds using the v1 defaults.
func New(bounds image.Rectangle) Grid {
	return Grid{
		Bounds: bounds,
		Cols:   DefaultCols,
		Rows:   DefaultRows,
		PadX:   DefaultPadding,
		PadY:   DefaultPadding,
		GapX:   DefaultGap,
		GapY:   DefaultGap,
	}
}

// CellSize returns the pixel size of a single 1x1 cell.
func (g Grid) CellSize() (w, h int) {
	if g.Cols <= 0 || g.Rows <= 0 {
		return 0, 0
	}
	availW := g.Bounds.Dx() - 2*g.PadX - g.GapX*(g.Cols-1)
	availH := g.Bounds.Dy() - 2*g.PadY - g.GapY*(g.Rows-1)
	return availW / g.Cols, availH / g.Rows
}

// Cell returns the pixel rectangle for a grid position.
//
// Out-of-range positions are clamped rather than rejected: a widget placed
// off the edge by a hand-edited config should end up somewhere visible, not
// vanish or panic. Validation surfaces the mistake separately.
func (g Grid) Cell(p Pos) image.Rectangle {
	cw, ch := g.CellSize()
	if cw <= 0 || ch <= 0 {
		return image.Rectangle{}
	}

	colSpan := max(1, p.ColSpan)
	rowSpan := max(1, p.RowSpan)
	col := clamp(p.Col, 0, g.Cols-1)
	row := clamp(p.Row, 0, g.Rows-1)
	colSpan = min(colSpan, g.Cols-col)
	rowSpan = min(rowSpan, g.Rows-row)

	x := g.Bounds.Min.X + g.PadX + col*(cw+g.GapX)
	y := g.Bounds.Min.Y + g.PadY + row*(ch+g.GapY)
	w := colSpan*cw + (colSpan-1)*g.GapX
	h := rowSpan*ch + (rowSpan-1)*g.GapY

	return image.Rect(x, y, x+w, y+h)
}

// Overlaps reports whether two positions share any cell. Used by config
// validation to warn about widgets stacked on top of each other.
func Overlaps(a, b Pos) bool {
	ar := image.Rect(a.Col, a.Row, a.Col+max(1, a.ColSpan), a.Row+max(1, a.RowSpan))
	br := image.Rect(b.Col, b.Row, b.Col+max(1, b.ColSpan), b.Row+max(1, b.RowSpan))
	return ar.Overlaps(br)
}

func clamp(v, lo, hi int) int {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}
