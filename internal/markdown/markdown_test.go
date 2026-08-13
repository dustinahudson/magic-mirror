package markdown

import (
	"strings"
	"testing"
	"time"
)

func kinds(bs []Block) []Kind {
	out := make([]Kind, len(bs))
	for i, b := range bs {
		out[i] = b.Kind
	}
	return out
}

func find(bs []Block, text string) *Block {
	for i := range bs {
		if bs[i].Text() == text {
			return &bs[i]
		}
	}
	return nil
}

func TestFlattensCommonMarkdown(t *testing.T) {
	blocks := Parse("# Shopping\n\n" +
		"- Milk\n" +
		"- **Eggs** free range\n" +
		"- [ ] Bread\n" +
		"- [x] Coffee\n\n" +
		"1. First\n2. Second\n\n" +
		"> Bins go out *Tuesday*\n\n" +
		"---\n\n" +
		"Plain paragraph.\n")

	if b := find(blocks, "Shopping"); b == nil || b.Kind != Heading || b.Level != 1 {
		t.Errorf("heading not recognised: %+v", b)
	}
	if b := find(blocks, "Milk"); b == nil || b.Kind != ListItem || b.Marker != "•" {
		t.Errorf("bullet not recognised: %+v", b)
	}
	if b := find(blocks, "First"); b == nil || b.Marker != "1." {
		t.Errorf("ordered marker = %+v, want 1.", b)
	}
	if b := find(blocks, "Second"); b == nil || b.Marker != "2." {
		t.Errorf("ordered numbering did not advance: %+v", b)
	}
	if b := find(blocks, "Plain paragraph."); b == nil || b.Kind != Paragraph {
		t.Errorf("paragraph not recognised: %+v", b)
	}

	var rules int
	for _, k := range kinds(blocks) {
		if k == Rule {
			rules++
		}
	}
	if rules != 1 {
		t.Errorf("got %d thematic breaks, want 1", rules)
	}
}

// Task lists are the reason this widget is useful on a fridge-adjacent
// mirror, so the checkbox state has to survive the flattening.
func TestTaskListState(t *testing.T) {
	blocks := Parse("- [ ] Bread\n- [x] Coffee\n")

	bread := find(blocks, "Bread")
	coffee := find(blocks, "Coffee")
	if bread == nil || coffee == nil {
		t.Fatalf("task items missing: %+v", blocks)
	}
	if bread.Kind != Task || coffee.Kind != Task {
		t.Errorf("kinds = %v/%v, want Task", bread.Kind, coffee.Kind)
	}
	if bread.Done {
		t.Error("unchecked task reported as done")
	}
	if !coffee.Done {
		t.Error("checked task reported as not done")
	}
	// The literal "[x]" must not survive into the text.
	if got := coffee.Text(); got != "Coffee" {
		t.Errorf("task text = %q, want %q", got, "Coffee")
	}
}

func TestInlineStyles(t *testing.T) {
	blocks := Parse("Normal **bold** and *italic* and `code`.\n")
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1", len(blocks))
	}

	var bold, italic, code bool
	for _, r := range blocks[0].Runs {
		switch r.Text {
		case "bold":
			bold = r.Style.Bold
		case "italic":
			italic = r.Style.Italic
		case "code":
			code = r.Style.Code
		}
	}
	if !bold {
		t.Error("**bold** did not set Bold")
	}
	if !italic {
		t.Error("*italic* did not set Italic")
	}
	if !code {
		t.Error("`code` did not set Code")
	}
}

// A link on a wall display can only ever be its label.
func TestLinksBecomeTheirText(t *testing.T) {
	blocks := Parse("See [the docs](https://example.com/very/long/url) for more.\n")
	got := blocks[0].Text()
	want := "See the docs for more."
	if got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

func TestNestedListIndents(t *testing.T) {
	blocks := Parse("- Top\n    - Nested\n")
	top, nested := find(blocks, "Top"), find(blocks, "Nested")
	if top == nil || nested == nil {
		t.Fatalf("items missing: %+v", blocks)
	}
	if nested.Level <= top.Level {
		t.Errorf("nested level %d not deeper than %d", nested.Level, top.Level)
	}
}

// Author line breaks are not meaningful when the panel decides the width.
func TestSoftBreaksBecomeSpaces(t *testing.T) {
	blocks := Parse("one\ntwo\nthree\n")
	if len(blocks) != 1 {
		t.Fatalf("got %d blocks, want 1 wrapped paragraph", len(blocks))
	}
	if got := blocks[0].Text(); got != "one two three" {
		t.Errorf("got %q, want %q", got, "one two three")
	}
}

// Malformed input must still produce something rather than nothing.
func TestGarbageStillRenders(t *testing.T) {
	for _, src := range []string{"", "***", "- \n- \n", "# ", "```\nunclosed", "|a|b|\n|-|"} {
		if got := Parse(src); got == nil && src != "" {
			t.Errorf("Parse(%q) returned nil", src)
		}
	}
}

// The note text is whatever somebody typed into the settings page, and this
// walk recurses once per level of nesting. A Go stack overflow is a fatal
// error that recover cannot catch — so unlike a widget that panics while
// rendering, this is not contained. And Notes parses at construction, inside
// buildPlacements on the render goroutine, from text saved in config.json:
// the crash would repeat on every boot, from a file the device reads before
// anyone can tell it otherwise.
func TestDeeplyNestedListsDoNotRecurseForever(t *testing.T) {
	// A megabyte of genuinely nested list, which is deep enough to recurse
	// past any stack that matters while staying a plausible paste.
	var b strings.Builder
	for i := range 1000 {
		b.WriteString(strings.Repeat("  ", i))
		b.WriteString("- deep\n")
	}

	done := make(chan int, 1)
	go func() {
		done <- len(Parse(b.String()))
	}()

	select {
	case n := <-done:
		if n > maxBlocks {
			t.Errorf("produced %d blocks, want at most %d", n, maxBlocks)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("still parsing after 30s")
	}

	// And the walk must have stopped descending, not merely survived.
	for _, blk := range Parse(b.String()) {
		if blk.Level > maxDepth {
			t.Errorf("emitted a block at depth %d, past the %d limit", blk.Level, maxDepth)
			break
		}
	}
}

// A note that is simply enormous must not turn into an unbounded block list
// that is then measured and drawn on every repaint.
func TestEnormousNoteIsCapped(t *testing.T) {
	var b strings.Builder
	for range 50000 {
		b.WriteString("a paragraph\n\n")
	}
	if n := len(Parse(b.String())); n > maxBlocks {
		t.Errorf("produced %d blocks, want at most %d", n, maxBlocks)
	}
}

// And ordinary notes must be completely unaffected, including nesting at the
// depth a person actually uses.
func TestOrdinaryNotesAreUnaffected(t *testing.T) {
	src := strings.Join([]string{
		"# This week",
		"",
		"- [x] Bins out Tuesday",
		"- [ ] Call the plumber",
		"  - ask about the boiler",
		"    - and the radiator",
		"",
		"> Remember the school run",
	}, "\n")

	blocks := Parse(src)
	if len(blocks) < 6 {
		t.Errorf("got %d blocks from an ordinary note, want at least 6", len(blocks))
	}

	var text string
	for _, b := range blocks {
		text += b.Text() + "|"
	}
	for _, want := range []string{"This week", "Bins out Tuesday", "Call the plumber",
		"ask about the boiler", "and the radiator", "Remember the school run"} {
		if !strings.Contains(text, want) {
			t.Errorf("ordinary note lost %q: %s", want, text)
		}
	}
}
