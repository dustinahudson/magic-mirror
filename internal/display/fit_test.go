package display

import (
	"image"
	"testing"
)

// A framebuffer whose reported geometry exceeds its mapping panics inside
// Present, on the render goroutine, where nothing recovers. These cases are
// the difference between a mirror with a black band at the bottom and a
// mirror that has to be taken off a wall.

func TestFitBoundsHonoursAWellFormedDevice(t *testing.T) {
	// A Pi at 1920x1080, 32bpp, exactly enough memory.
	got, err := fitBounds(1920, 1080, 1920*4, 4, 1920*4*1080)
	if err != nil {
		t.Fatal(err)
	}
	if want := image.Rect(0, 0, 1920, 1080); got != want {
		t.Errorf("bounds = %v, want %v", got, want)
	}
}

// Padded strides are normal: the line length exceeds the visible width.
func TestFitBoundsAcceptsAPaddedStride(t *testing.T) {
	stride := 2048 * 4
	got, err := fitBounds(1920, 1080, stride, 4, stride*1080)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dx() != 1920 || got.Dy() != 1080 {
		t.Errorf("bounds = %v, want the full 1920x1080", got)
	}
}

// The case that would panic: more rows claimed than mapped.
func TestFitBoundsClampsToTheMapping(t *testing.T) {
	stride := 1920 * 4
	// Memory for 900 rows, geometry claiming 1080.
	got, err := fitBounds(1920, 1080, stride, 4, stride*900)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dy() != 900 {
		t.Errorf("height = %d, want it clamped to the 900 rows actually mapped", got.Dy())
	}
	if got.Dx() != 1920 {
		t.Errorf("width = %d, want the full width kept", got.Dx())
	}
	// The clamped rectangle must be entirely addressable.
	if last := (got.Dy()-1)*stride + (got.Dx()-1)*4 + 3; last >= stride*900 {
		t.Errorf("the last pixel of the clamped bounds still lies outside the mapping")
	}
}

// Off by a single row is the realistic version of the same mistake.
func TestFitBoundsClampsASingleMissingRow(t *testing.T) {
	stride := 1920 * 4
	got, err := fitBounds(1920, 1080, stride, 4, stride*1080-1)
	if err != nil {
		t.Fatal(err)
	}
	if got.Dy() != 1079 {
		t.Errorf("height = %d, want 1079", got.Dy())
	}
}

// A line length that cannot hold the width would wrap every row into the next
// one. That is not something to render short — it is a device this code does
// not understand, and saying so beats drawing a diagonal smear.
func TestFitBoundsRejectsAStrideTooNarrowForTheWidth(t *testing.T) {
	if _, err := fitBounds(1920, 1080, 1000, 4, 1000*1080); err == nil {
		t.Fatal("accepted a line length too narrow for the reported width")
	}
}

func TestFitBoundsRejectsDegenerateDevices(t *testing.T) {
	cases := []struct {
		name                      string
		w, h, stride, bpp, memLen int
	}{
		{"zero stride", 1920, 1080, 0, 4, 1 << 20},
		{"negative stride", 1920, 1080, -4, 4, 1 << 20},
		{"zero width", 0, 1080, 1920 * 4, 4, 1 << 20},
		{"zero height", 1920, 0, 1920 * 4, 4, 1 << 20},
		{"zero bpp", 1920, 1080, 1920 * 4, 0, 1 << 20},
		{"mapping smaller than one line", 1920, 1080, 1920 * 4, 4, 100},
		{"empty mapping", 1920, 1080, 1920 * 4, 4, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := fitBounds(tc.w, tc.h, tc.stride, tc.bpp, tc.memLen); err == nil {
				t.Error("accepted a device that cannot be addressed safely")
			}
		})
	}
}

// Whatever fitBounds returns must be writable without a bounds check failing,
// which is the entire property being bought here.
func TestFitBoundsResultIsAlwaysAddressable(t *testing.T) {
	cases := [][5]int{
		{1920, 1080, 1920 * 4, 4, 1920 * 4 * 1080},
		{1920, 1080, 1920 * 4, 4, 1920 * 4 * 900},
		{640, 480, 640 * 2, 2, 640 * 2 * 480},
		{640, 480, 1024 * 2, 2, 1024 * 2 * 200},
		{800, 600, 800 * 3, 3, 800 * 3 * 599},
	}
	for _, c := range cases {
		w, h, stride, bpp, memLen := c[0], c[1], c[2], c[3], c[4]
		got, err := fitBounds(w, h, stride, bpp, memLen)
		if err != nil {
			t.Fatalf("fitBounds(%v) = %v", c, err)
		}
		// The furthest byte any blit can touch.
		last := (got.Dy()-1)*stride + (got.Dx()-1)*bpp + (bpp - 1)
		if last >= memLen {
			t.Errorf("fitBounds(%v) = %v, whose last byte %d is outside the %d-byte mapping",
				c, got, last, memLen)
		}
	}
}
