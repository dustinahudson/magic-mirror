//go:build linux

package display

import (
	"fmt"
	"image"
	"os"
	"unsafe"

	"golang.org/x/sys/unix"
)

// Linux fbdev ioctls.
const (
	fbioGetVScreenInfo = 0x4600
	fbioGetFScreenInfo = 0x4602
	fbioPanDisplay     = 0x4606
)

type fbBitfield struct {
	Offset   uint32
	Length   uint32
	MSBRight uint32
}

// fbVarScreenInfo mirrors struct fb_var_screeninfo from <linux/fb.h>.
type fbVarScreenInfo struct {
	Xres, Yres               uint32
	XresVirtual, YresVirtual uint32
	Xoffset, Yoffset         uint32
	BitsPerPixel             uint32
	Grayscale                uint32
	Red, Green, Blue, Transp fbBitfield
	Nonstd                   uint32
	Activate                 uint32
	Height, Width            uint32
	AccelFlags               uint32
	Pixclock                 uint32
	LeftMargin, RightMargin  uint32
	UpperMargin, LowerMargin uint32
	HsyncLen, VsyncLen       uint32
	Sync, Vmode, Rotate      uint32
	Colorspace               uint32
	Reserved                 [4]uint32
}

// fbFixScreenInfo mirrors struct fb_fix_screeninfo from <linux/fb.h>.
// smem_start and mmio_start are `unsigned long`, so uintptr keeps this
// correct on both the 32-bit ARM target and a 64-bit dev host.
type fbFixScreenInfo struct {
	ID         [16]byte
	SmemStart  uintptr
	SmemLen    uint32
	Type       uint32
	TypeAux    uint32
	Visual     uint32
	Xpanstep   uint16
	Ypanstep   uint16
	Ywrapstep  uint16
	_          uint16 // padding to align LineLength
	LineLength uint32
	MmioStart  uintptr
	MmioLen    uint32
	Accel      uint32
	Caps       uint16
	Reserved   [2]uint16
	_          uint16
}

// FrameBuffer is a Display backed by a Linux fbdev device.
//
// Pixels are converted from the renderer's RGBA into whatever layout the
// device reports, driven by the red/green/blue bitfields rather than assuming
// a format. Fast paths cover the two layouts a Pi actually produces (32bpp
// with 8-bit channels, and RGB565); anything else falls back to a generic
// shift-and-mask path so an unexpected panel degrades in speed, not
// correctness.
type FrameBuffer struct {
	f      *os.File
	mem    []byte
	bounds image.Rectangle

	stride int // bytes per line on the device
	bpp    int // bytes per pixel
	vinfo  fbVarScreenInfo

	// Precomputed channel placement.
	rOff, gOff, bOff uint32
	rLen, gLen, bLen uint32
	fast32           bool // 8/8/8 channels in a 4-byte pixel
	fast565          bool

	// clamped records that the mapping was smaller than the geometry, so
	// Describe can say so rather than leaving a short display unexplained.
	clamped bool
}

// OpenFrameBuffer opens a framebuffer device, e.g. "/dev/fb0".
func OpenFrameBuffer(path string) (*FrameBuffer, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", path, err)
	}

	var vinfo fbVarScreenInfo
	if err := ioctl(f.Fd(), fbioGetVScreenInfo, unsafe.Pointer(&vinfo)); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: FBIOGET_VSCREENINFO: %w", path, err)
	}

	var finfo fbFixScreenInfo
	if err := ioctl(f.Fd(), fbioGetFScreenInfo, unsafe.Pointer(&finfo)); err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: FBIOGET_FSCREENINFO: %w", path, err)
	}

	if vinfo.BitsPerPixel%8 != 0 || vinfo.BitsPerPixel < 16 {
		f.Close()
		return nil, fmt.Errorf("%s: unsupported depth %d bpp", path, vinfo.BitsPerPixel)
	}

	mem, err := unix.Mmap(int(f.Fd()), 0, int(finfo.SmemLen),
		unix.PROT_READ|unix.PROT_WRITE, unix.MAP_SHARED)
	if err != nil {
		f.Close()
		return nil, fmt.Errorf("%s: mmap %d bytes: %w", path, finfo.SmemLen, err)
	}

	bpp := int(vinfo.BitsPerPixel) / 8
	stride := int(finfo.LineLength)
	width, height := int(vinfo.Xres), int(vinfo.Yres)

	bounds, err := fitBounds(width, height, stride, bpp, len(mem))
	if err != nil {
		f.Close()
		_ = unix.Munmap(mem)
		return nil, fmt.Errorf("%s: %w", path, err)
	}

	fb := &FrameBuffer{
		f:      f,
		mem:    mem,
		bounds: bounds,
		stride: stride,
		bpp:    bpp,
		vinfo:  vinfo,
		rOff:   vinfo.Red.Offset, gOff: vinfo.Green.Offset, bOff: vinfo.Blue.Offset,
		rLen: vinfo.Red.Length, gLen: vinfo.Green.Length, bLen: vinfo.Blue.Length,
	}
	fb.clamped = bounds.Dy() != int(vinfo.Yres)

	fb.fast32 = fb.bpp == 4 && fb.rLen == 8 && fb.gLen == 8 && fb.bLen == 8
	fb.fast565 = fb.bpp == 2 &&
		fb.rOff == 11 && fb.rLen == 5 &&
		fb.gOff == 5 && fb.gLen == 6 &&
		fb.bOff == 0 && fb.bLen == 5

	return fb, nil
}

// Describe reports the detected geometry and pixel layout. Logged at startup
// because a wrong-looking display is nearly always a format mismatch, and
// this is the one line that tells you.
func (fb *FrameBuffer) Describe() string {
	path := "generic"
	switch {
	case fb.fast32:
		path = "fast32"
	case fb.fast565:
		path = "fast565"
	}
	note := ""
	if fb.clamped {
		note = fmt.Sprintf(" CLAMPED from %d rows: the mapping is smaller than the reported geometry",
			fb.vinfo.Yres)
	}
	return fmt.Sprintf("%dx%d %dbpp stride=%d r=%d/%d g=%d/%d b=%d/%d (%s)%s",
		fb.bounds.Dx(), fb.bounds.Dy(), fb.bpp*8, fb.stride,
		fb.rOff, fb.rLen, fb.gOff, fb.gLen, fb.bOff, fb.bLen, path, note)
}

func (fb *FrameBuffer) Bounds() image.Rectangle { return fb.bounds }

func (fb *FrameBuffer) Present(frame *image.RGBA, dirty []image.Rectangle) error {
	// A full 1920x1080 conversion is millions of pixels; on an ARMv6 core
	// that is the difference between a smooth clock and a stuttering one.
	// Honour the dirty list.
	if len(dirty) == 0 {
		fb.blit(frame, fb.bounds)
		return nil
	}
	for _, r := range dirty {
		r = r.Intersect(fb.bounds)
		if !r.Empty() {
			fb.blit(frame, r)
		}
	}
	return nil
}

func (fb *FrameBuffer) blit(src *image.RGBA, r image.Rectangle) {
	switch {
	case fb.fast32:
		fb.blit32(src, r)
	case fb.fast565:
		fb.blit565(src, r)
	default:
		fb.blitGeneric(src, r)
	}
}

// blit32 handles any 4-byte pixel with 8-bit channels by placing bytes at the
// offsets the device reported — covers both XRGB8888 and BGRX8888 without
// branching per pixel.
func (fb *FrameBuffer) blit32(src *image.RGBA, r image.Rectangle) {
	rb, gb, bb := int(fb.rOff/8), int(fb.gOff/8), int(fb.bOff/8)
	for y := r.Min.Y; y < r.Max.Y; y++ {
		si := src.PixOffset(r.Min.X, y)
		di := y*fb.stride + r.Min.X*4
		for x := r.Min.X; x < r.Max.X; x++ {
			fb.mem[di+rb] = src.Pix[si]
			fb.mem[di+gb] = src.Pix[si+1]
			fb.mem[di+bb] = src.Pix[si+2]
			si += 4
			di += 4
		}
	}
}

func (fb *FrameBuffer) blit565(src *image.RGBA, r image.Rectangle) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		si := src.PixOffset(r.Min.X, y)
		di := y*fb.stride + r.Min.X*2
		for x := r.Min.X; x < r.Max.X; x++ {
			v := uint16(src.Pix[si]>>3)<<11 |
				uint16(src.Pix[si+1]>>2)<<5 |
				uint16(src.Pix[si+2]>>3)
			fb.mem[di] = byte(v)
			fb.mem[di+1] = byte(v >> 8)
			si += 4
			di += 2
		}
	}
}

func (fb *FrameBuffer) blitGeneric(src *image.RGBA, r image.Rectangle) {
	for y := r.Min.Y; y < r.Max.Y; y++ {
		si := src.PixOffset(r.Min.X, y)
		di := y*fb.stride + r.Min.X*fb.bpp
		for x := r.Min.X; x < r.Max.X; x++ {
			v := scale(uint32(src.Pix[si]), fb.rLen)<<fb.rOff |
				scale(uint32(src.Pix[si+1]), fb.gLen)<<fb.gOff |
				scale(uint32(src.Pix[si+2]), fb.bLen)<<fb.bOff
			for b := 0; b < fb.bpp; b++ {
				fb.mem[di+b] = byte(v >> (8 * b))
			}
			si += 4
			di += fb.bpp
		}
	}
}

// scale reduces an 8-bit channel to n bits.
func scale(v, n uint32) uint32 {
	if n >= 8 {
		return v
	}
	return v >> (8 - n)
}

func (fb *FrameBuffer) Close() error {
	if fb.mem != nil {
		_ = unix.Munmap(fb.mem)
		fb.mem = nil
	}
	return fb.f.Close()
}

func ioctl(fd uintptr, req uint, arg unsafe.Pointer) error {
	_, _, errno := unix.Syscall(unix.SYS_IOCTL, fd, uintptr(req), uintptr(arg))
	if errno != 0 {
		return errno
	}
	return nil
}
