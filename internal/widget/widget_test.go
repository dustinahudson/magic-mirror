package widget

import (
	"encoding/json"
	"image"
	"strings"
	"testing"
	"time"
)

type probe struct{ cfg map[string]any }

func (p *probe) Key(Context) string                           { return "probe" }
func (p *probe) Render(*image.RGBA, image.Rectangle, Context) {}

func init() {
	Register(Descriptor{
		Type: "test_defaults",
		Name: "Test Defaults",
		Fields: []Field{
			{Key: "text", Type: FieldMultiline, Default: "hello"},
			{Key: "size", Type: FieldNumber, Default: 20},
			{Key: "on", Type: FieldBool, Default: true},
			{Key: "bare", Type: FieldText},
		},
		New: func(raw json.RawMessage) (Widget, error) {
			p := &probe{}
			if len(raw) > 0 {
				if err := json.Unmarshal(raw, &p.cfg); err != nil {
					return nil, err
				}
			}
			return p, nil
		},
	})
}

func build(t *testing.T, raw string) map[string]any {
	t.Helper()
	w := Build("test_defaults", json.RawMessage(raw))
	p, ok := w.(*probe)
	if !ok {
		t.Fatalf("Build returned %T: %+v", w, w)
	}
	return p.cfg
}

// A panel added in the web UI and left alone is stored as "{}": the form
// displays defaults but only records a value once a field is edited. The
// widget must still be built with them, or it renders blank.
func TestBuildAppliesDefaultsToEmptyConfig(t *testing.T) {
	for _, raw := range []string{"{}", ""} {
		cfg := build(t, raw)
		if cfg["text"] != "hello" {
			t.Errorf("raw %q: text = %v, want hello", raw, cfg["text"])
		}
		if cfg["size"] != float64(20) {
			t.Errorf("raw %q: size = %v, want 20", raw, cfg["size"])
		}
		if cfg["on"] != true {
			t.Errorf("raw %q: on = %v, want true", raw, cfg["on"])
		}
		if _, ok := cfg["bare"]; ok {
			t.Errorf("raw %q: field without a default was invented: %v", raw, cfg["bare"])
		}
	}
}

// Clearing a field is a choice. An empty string, a zero and a false are all
// values a user can mean, so a present key is never overwritten.
func TestBuildKeepsExplicitValues(t *testing.T) {
	cfg := build(t, `{"text":"","size":0,"on":false}`)
	if cfg["text"] != "" {
		t.Errorf("text = %v, want empty", cfg["text"])
	}
	if cfg["size"] != float64(0) {
		t.Errorf("size = %v, want 0", cfg["size"])
	}
	if cfg["on"] != false {
		t.Errorf("on = %v, want false", cfg["on"])
	}
}

func TestBuildPartialConfigFillsTheRest(t *testing.T) {
	cfg := build(t, `{"size":40}`)
	if cfg["size"] != float64(40) {
		t.Errorf("size = %v, want 40", cfg["size"])
	}
	if cfg["text"] != "hello" {
		t.Errorf("text = %v, want hello", cfg["text"])
	}
}

// A config written by a newer binary, then rolled back, must not stop the
// mirror booting.
func TestBuildUnknownTypeIsPlaceholder(t *testing.T) {
	if _, ok := Build("no_such_widget", nil).(*Unknown); !ok {
		t.Fatal("unknown type did not yield a placeholder")
	}
}

func TestBuildMalformedConfigIsPlaceholder(t *testing.T) {
	if _, ok := Build("test_defaults", json.RawMessage(`[1,2]`)).(*Unknown); !ok {
		t.Fatal("malformed config did not yield a placeholder")
	}
}

// A constructor that panics must produce a labelled tile, not a dead process.
//
// The compositor already isolates a widget that panics while rendering.
// Construction is the riskier half — constructors parse configuration written
// through the web UI and text pasted from anywhere — and it runs inside
// buildPlacements on the render goroutine. Since the configuration that
// caused it is saved, an unhandled panic there is not one bad frame: it is a
// crash on this boot and every boot after, from a house nobody can visit.
func TestBuildContainsAPanickingConstructor(t *testing.T) {
	const typ = "exploding-test-widget"
	Register(Descriptor{
		Type: typ,
		Name: "Exploding",
		New: func(json.RawMessage) (Widget, error) {
			panic("the constructor exploded")
		},
	})

	w := Build(typ, json.RawMessage(`{}`))
	if w == nil {
		t.Fatal("Build returned nil for a panicking constructor")
	}
	u, ok := w.(*Unknown)
	if !ok {
		t.Fatalf("got %T, want a *Unknown placeholder", w)
	}
	if !strings.Contains(u.Reason, "panicked") {
		t.Errorf("reason = %q, want it to say the constructor panicked", u.Reason)
	}

	// And the placeholder has to survive being rendered and keyed, or the
	// containment merely moves the crash one step later.
	if k := w.Key(Context{Now: time.Now(), Loc: time.UTC}); k == "" {
		t.Error("the placeholder produced an empty key")
	}
}
