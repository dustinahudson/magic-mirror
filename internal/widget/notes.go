package widget

import (
	"encoding/json"
	"image"
	"strings"

	"github.com/dustinahudson/magic-mirror/internal/layout"
	"github.com/dustinahudson/magic-mirror/internal/markdown"
	"github.com/dustinahudson/magic-mirror/internal/render"
)

func init() {
	Register(Descriptor{
		Type:        "notes",
		Name:        "Notes",
		Description: "A shared note, written in markdown. Lists, headings and checkboxes.",
		DefaultSpan: layout.Span{Cols: 3, Rows: 5},
		MinSpan:     layout.Span{Cols: 2, Rows: 2},
		Fields: []Field{
			{
				Key: "text", Label: "Note", Type: FieldMultiline,
				Default: "## This week\n\n- [ ] Bins out Tuesday\n- [ ] Call the plumber\n",
				Help: "Markdown: # for a heading, - for a bullet, - [ ] for a checkbox, " +
					"**bold**, *italic*.",
			},
			{
				Key: "size", Label: "Text size (px)", Type: FieldNumber,
				Default: 20, Min: f64(10), Max: f64(64),
			},
		},
		New: newNotes,
	})
}

type notesConfig struct {
	Text string `json:"text"`
	Size int    `json:"size"`
}

// Notes renders a markdown note.
//
// Parsing is goldmark's, flattened by internal/markdown into blocks. What
// happens here is layout: wrapping styled words to the panel width, and
// deciding what a heading or a checkbox looks like on a mirror.
type Notes struct {
	cfg    notesConfig
	blocks []markdown.Block
}

func newNotes(raw json.RawMessage) (Widget, error) {
	cfg := notesConfig{Size: 20}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return nil, err
		}
	}
	if cfg.Size < 10 {
		cfg.Size = 20
	}
	// Parsed once at construction rather than per frame. The text only
	// changes when the config does, and a config change rebuilds the widget.
	return &Notes{cfg: cfg, blocks: markdown.Parse(cfg.Text)}, nil
}

// Key is the note itself: the content is static, so the panel only repaints
// when the note is edited or the panel is resized.
func (w *Notes) Key(Context) string {
	return "notes|" + w.cfg.Text
}

func (w *Notes) Render(dst *image.RGBA, bounds image.Rectangle, ctx Context) {
	if len(w.blocks) == 0 {
		return
	}

	base := w.cfg.Size
	body, err := ctx.Fonts.Face(render.Regular, base)
	if err != nil {
		return
	}

	y := bounds.Min.Y
	indentW := body.Measure("  ")

	for _, b := range w.blocks {
		if y >= bounds.Max.Y {
			break
		}

		switch b.Kind {
		case markdown.Rule:
			y += body.Height() / 3
			if y < bounds.Max.Y {
				render.HLine(dst, bounds.Min.X, bounds.Max.X, y, 1, render.Faint)
			}
			y += body.Height() / 3
			continue

		case markdown.Heading:
			// Levels beyond 3 stop growing: a mirror has no room for a
			// six-level hierarchy, and h4 through h6 read as bold body text.
			scale := []int{170, 135, 115, 100, 100, 100}[clampInt(b.Level, 1, 6)-1]
			size := max(base, base*scale/100)
			f, err := ctx.Fonts.Face(render.SemiBold, size)
			if err != nil {
				continue
			}
			y += size / 4
			y = w.drawWrapped(dst, b, f, ctx, bounds, y, bounds.Min.X, render.Primary)
			continue
		}

		x := bounds.Min.X + b.Level*indentW
		colour := render.Secondary

		switch b.Kind {
		case markdown.Quote:
			// A quote gets a rule down its left edge rather than an indent
			// plus quotation marks, which is quieter at a distance.
			render.Fill(dst, image.Rect(x, y, x+2, min(y+body.Height(), bounds.Max.Y)), render.Faint)
			x += body.Measure(" ") * 2
			colour = render.Muted

		case markdown.Code:
			colour = render.Muted

		case markdown.Task:
			// Checkboxes are drawn rather than typed, so they line up and do
			// not depend on the font carrying ballot-box glyphs.
			boxY := y + (body.Height()-body.Size()*2/3)/2
			box := image.Rect(x, boxY, x+body.Size()*2/3, boxY+body.Size()*2/3)
			if b.Done {
				render.FillRounded(dst, box, 3, render.Muted)
				drawTick(dst, box, render.Background)
				colour = render.Faint
			} else {
				render.Stroke(dst, box, 1, render.Muted)
			}
			x = box.Max.X + body.Measure(" ")

		case markdown.ListItem:
			if b.Marker != "" {
				x = body.DrawTop(dst, x, y, b.Marker, render.Muted) + body.Measure(" ")
			}
		}

		y = w.drawWrapped(dst, b, body, ctx, bounds, y, x, colour)
	}
}

// drawTick draws a checkmark inside box.
//
// Stepped rather than two rectangles: at the sizes a mirror uses, a
// horizontal bar plus a vertical bar reads as a blob. Walking down-right
// then up-right with small squares gives a shape recognisable as a tick
// even at twelve pixels.
func drawTick(dst *image.RGBA, box image.Rectangle, c render.RGBA) {
	n := box.Dx()
	if n < 6 {
		render.Fill(dst, box.Inset(box.Dx()/3), c)
		return
	}
	t := max(1, n/7) // stroke thickness

	// Down-right from the left third to the bottom-centre.
	x, y := box.Min.X+n/4, box.Min.Y+n/2
	for range n / 4 {
		render.Fill(dst, image.Rect(x, y, x+t, y+t), c)
		x++
		y++
	}
	// Up-right to the top-right corner.
	for range n / 2 {
		render.Fill(dst, image.Rect(x, y, x+t, y+t), c)
		x++
		y--
	}
}

// drawWrapped lays out a block's styled words, wrapping to the panel width,
// and returns the y below it.
//
// Wrapping happens per word rather than per block because a block mixes
// faces: "a **bold** word" measures differently in three pieces than as one
// string, and measuring the whole thing in the body face would wrap in the
// wrong place.
func (w *Notes) drawWrapped(dst *image.RGBA, b markdown.Block, base *render.Face,
	ctx Context, bounds image.Rectangle, y, x0 int, colour render.RGBA) int {

	type word struct {
		text  string
		face  *render.Face
		color render.RGBA
	}

	faceFor := func(s markdown.Style) (*render.Face, render.RGBA) {
		c := colour
		weight := render.Regular
		switch {
		case s.Bold:
			weight = render.SemiBold
			c = render.Primary
		case s.Italic:
			weight = render.Italic
		case s.Code:
			c = render.Muted
		}
		f, err := ctx.Fonts.Face(weight, base.Size())
		if err != nil {
			return base, c
		}
		return f, c
	}

	// Build a character-indexed style map, then split on whitespace across
	// the whole block.
	//
	// Splitting each run separately loses whether a token abutted the
	// previous one: "*guest-2G*," is two runs, and inserting a space
	// between every pair rendered it as "guest-2G ,". Splitting the joined
	// text and looking styles up by offset keeps punctuation attached.
	var sb strings.Builder
	var styles []markdown.Style
	for _, r := range b.Runs {
		sb.WriteString(r.Text)
		for range r.Text {
			styles = append(styles, r.Style)
		}
	}
	full := sb.String()

	var words []word
	for i := 0; i < len(full); {
		if full[i] == ' ' || full[i] == '\t' || full[i] == '\n' {
			i++
			continue
		}
		j := i
		for j < len(full) && full[j] != ' ' && full[j] != '\t' && full[j] != '\n' {
			j++
		}
		st := markdown.Style{}
		if i < len(styles) {
			st = styles[i]
		}
		f, c := faceFor(st)
		if b.Kind == markdown.Heading {
			f, c = base, render.Primary
		}
		words = append(words, word{text: full[i:j], face: f, color: c})
		i = j
	}
	if len(words) == 0 {
		return y + base.Height()
	}

	space := base.Measure(" ")
	x := x0

	for i, wd := range words {
		width := wd.face.Measure(wd.text)

		// Wrap, but never leave a word alone on a line it cannot fit on —
		// truncate it instead, or a long URL would loop forever.
		if i > 0 && x+space+width > bounds.Max.X {
			y += base.Height()
			x = x0
			if y+base.Height() > bounds.Max.Y {
				return y
			}
		} else if i > 0 {
			x += space
		}

		if width > bounds.Max.X-x {
			wd.text = wd.face.Truncate(wd.text, bounds.Max.X-x)
		}
		x = wd.face.DrawTop(dst, x, y, wd.text, wd.color)
	}

	return y + base.Height()
}
