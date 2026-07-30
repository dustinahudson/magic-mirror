package markdown

import "testing"

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
