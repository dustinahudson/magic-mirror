// Package markdown turns markdown into blocks a framebuffer can draw.
//
// Parsing is goldmark's; what happens here is the flattening. goldmark
// produces a tree meant for HTML, where nesting and reflow are someone
// else's problem. A framebuffer has neither, so the tree is reduced to a
// flat list of blocks, each carrying styled word runs and an indent level.
// The widget then only has to walk a list and wrap words.
//
// The supported subset is deliberately what suits a mirror on a wall:
// headings, paragraphs, bullet and numbered lists, task lists, quotes and
// rules. Tables, images, links and HTML are flattened to their text — a
// wall display cannot be clicked, so a link is only ever its label.
package markdown

import (
	"bytes"
	"strconv"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	east "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
)

// Kind is what sort of block this is.
type Kind int

const (
	Paragraph Kind = iota
	Heading
	ListItem
	Task
	Quote
	Rule
	Code
)

// Style is the inline styling of one run of text.
type Style struct {
	Bold   bool
	Italic bool
	Code   bool
}

// Run is a stretch of text sharing one style.
type Run struct {
	Text  string
	Style Style
}

// Block is one drawable line-group.
type Block struct {
	Kind  Kind
	Level int // heading level 1-6, or list nesting depth
	Runs  []Run
	Done  bool // task list checkbox state

	// Marker is the bullet or number drawn in the gutter, already
	// formatted — "•", "3.", or empty.
	Marker string
}

// Text is the block's content with styling dropped, for measuring and for
// dirty-checking.
func (b Block) Text() string {
	var out []byte
	for _, r := range b.Runs {
		out = append(out, r.Text...)
	}
	return string(out)
}

// Parse converts markdown source into blocks.
//
// Never fails: malformed markdown is still text, and a notes panel that
// renders nothing because of a stray character would be worse than one
// showing the character.
func Parse(src string) []Block {
	md := goldmark.New(goldmark.WithExtensions(extension.TaskList, extension.Strikethrough))
	doc := md.Parser().Parse(text.NewReader([]byte(src)))

	var out []Block
	walk(doc, []byte(src), &out, 0, nil)
	return out
}

// walk flattens the tree.
//
// listCounter is threaded through so ordered lists number correctly even
// when nested, which the tree structure alone does not give us.
func walk(n ast.Node, src []byte, out *[]Block, depth int, counter *int) {
	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch node := c.(type) {

		case *ast.Heading:
			*out = append(*out, Block{
				Kind:  Heading,
				Level: node.Level,
				Runs:  inline(node, src, Style{}),
			})

		case *ast.Paragraph, *ast.TextBlock:
			runs := inline(c, src, Style{})
			if len(runs) > 0 {
				*out = append(*out, Block{Kind: Paragraph, Level: depth, Runs: runs})
			}

		case *ast.List:
			num := node.Start
			if num == 0 {
				num = 1
			}
			for item := node.FirstChild(); item != nil; item = item.NextSibling() {
				marker := "•"
				if node.IsOrdered() {
					marker = strconv.Itoa(num) + "."
					num++
				}
				emitItem(item, src, out, depth, marker)
			}

		case *ast.Blockquote:
			// Quotes are flattened rather than nested: one level of visual
			// indent is all a mirror can spare.
			var inner []Block
			walk(c, src, &inner, depth+1, counter)
			for _, b := range inner {
				b.Kind = Quote
				b.Level = depth
				*out = append(*out, b)
			}

		case *ast.ThematicBreak:
			*out = append(*out, Block{Kind: Rule})

		case *ast.FencedCodeBlock, *ast.CodeBlock:
			var buf bytes.Buffer
			lines := c.Lines()
			for i := range lines.Len() {
				seg := lines.At(i)
				buf.Write(seg.Value(src))
			}
			for _, line := range bytes.Split(bytes.TrimRight(buf.Bytes(), "\n"), []byte("\n")) {
				*out = append(*out, Block{
					Kind: Code, Level: depth,
					Runs: []Run{{Text: string(line), Style: Style{Code: true}}},
				})
			}

		default:
			walk(c, src, out, depth, counter)
		}
	}
}

// emitItem turns one list item into a block, detecting task checkboxes.
func emitItem(item ast.Node, src []byte, out *[]Block, depth int, marker string) {
	block := Block{Kind: ListItem, Level: depth, Marker: marker}

	// A task list item carries a checkbox as the first child of its text
	// block; goldmark reports its state rather than leaving "[x]" in the
	// text.
	if tb := item.FirstChild(); tb != nil {
		if cb, ok := tb.FirstChild().(*east.TaskCheckBox); ok {
			block.Kind = Task
			block.Done = cb.IsChecked
			block.Marker = ""
		}
	}

	// Emitted even with no text. Someone typing a list into the settings
	// box has an empty item at the moment they press return, and a bullet
	// that appears immediately is feedback that the list is being
	// understood — where rendering nothing looks like the panel is broken.
	block.Runs = inline(item, src, Style{})
	*out = append(*out, block)

	// Nested lists become deeper items rather than nested structures.
	for c := item.FirstChild(); c != nil; c = c.NextSibling() {
		if list, ok := c.(*ast.List); ok {
			num := list.Start
			if num == 0 {
				num = 1
			}
			for sub := list.FirstChild(); sub != nil; sub = sub.NextSibling() {
				m := "◦"
				if list.IsOrdered() {
					m = strconv.Itoa(num) + "."
					num++
				}
				emitItem(sub, src, out, depth+1, m)
			}
		}
	}
}

// inline collects styled text runs, skipping anything block-level.
func inline(n ast.Node, src []byte, style Style) []Run {
	var runs []Run

	for c := n.FirstChild(); c != nil; c = c.NextSibling() {
		switch node := c.(type) {
		case *ast.Text:
			t := string(node.Segment.Value(src))
			if t != "" {
				runs = append(runs, Run{Text: t, Style: style})
			}
			// A soft or hard break inside a paragraph becomes a space: the
			// widget wraps to the panel width, so the author's line endings
			// are not meaningful.
			if node.SoftLineBreak() || node.HardLineBreak() {
				runs = append(runs, Run{Text: " ", Style: style})
			}

		case *ast.Emphasis:
			s := style
			if node.Level >= 2 {
				s.Bold = true
			} else {
				s.Italic = true
			}
			runs = append(runs, inline(c, src, s)...)

		case *ast.CodeSpan:
			s := style
			s.Code = true
			runs = append(runs, inline(c, src, s)...)

		case *ast.Link, *ast.AutoLink:
			// A wall display cannot be clicked, so a link is its label.
			runs = append(runs, inline(c, src, style)...)

		case *east.TaskCheckBox:
			// Reported on the block instead.

		case *ast.List, *ast.Paragraph, *ast.TextBlock:
			if _, isList := c.(*ast.List); isList {
				continue // handled by emitItem
			}
			runs = append(runs, inline(c, src, style)...)

		default:
			runs = append(runs, inline(c, src, style)...)
		}
	}
	return runs
}
