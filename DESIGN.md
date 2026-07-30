# Magic Mirror v2 — Buildroot + Go

Rewrite of the bare-metal Circle OS mirror (v0.14.0) onto a minimal Buildroot Linux
image running a single static Go binary.

## Why

Three field failures drove this, and they share one root cause.

In `src/core/kernel.cpp`, the main loop calls `calService.FetchCalendar(...)` (line 489)
— TLS handshake, hundreds of KB of ICS download, parse — synchronously, and calls
`m_LVGL.Update(FALSE)` (line 577), the entire render tick, at the bottom of the same
function body. **The render loop and the network fetch are one thread of control.**

Everything follows from that:

| Symptom | Mechanism |
|---|---|
| Screen freezes during refresh | The clock cannot redraw until `FetchCalendar` returns. Not tuning — shape. |
| API down ⇒ boot loop | `m_Watchdog.Start(15)` (line 600) arms a 15s hardware watchdog per iteration. A hung API blocks past 15s ⇒ reboot ⇒ fetch again ⇒ hang. The watchdog cannot distinguish "wedged" from "waiting on a slow server," because here they are the same state. |
| Network drop ⇒ hard reset | `WiFiMonitor` escalates Healthy→Degraded→Kicked→**Dead→reboot** (line 452). Reboot is the *first* recovery tool because nothing runs beneath the app. When reboot doesn't take, the power cable is what's left. |
| Failures are invisible | `weather_widget.cpp:38-48` seeds a complete fabricated record — 72°F, "Partly Cloudy", Dallas TX, sunrise 6:45am. A failed fetch renders as confident, plausible, invented weather. You cannot tell working from broken by looking at the mirror, including during a hung fetch. |

Secondary: nothing is testable without flashing an SD card. `src/core/application.cpp` is
vestigial (its `Render()` draws a debug grid), `font_renderer.cpp` and `loading_screen.cpp`
aren't in the Makefile's `OBJS` at all, and there are two separate `http_client.cpp` files.

## Goals

1. Minimum viable image size for a Pi Zero W with networking and display.
2. Boots straight into a Go app carrying the current display design.
3. Serves a config webpage.
4. **A network or API fault never stalls a frame and never reboots the device.**
5. Recoverable in the field without a card reader.

## Decisions

| Area | Decision |
|---|---|
| Rendering | Pure Go → `/dev/fb0`. No cgo, no LVGL. |
| Provisioning | Boot-partition file first, hostapd AP portal as fallback. |
| Updates | Binary swap with automatic rollback to previous binary. |
| Repo | Clean replacement on branch `buildroot-go`; history preserved, old build at tag `v0.14.0`. |

Rendering rationale: LVGL requires all `lv_*` calls on one thread, so the cgo route forces
fetched data across a lock into the UI thread — reintroducing exactly the coupling that
causes the freeze. Pure Go lets the renderer own the framebuffer outright, with no
HTTP client in scope.

## The core invariant

```go
// Render loop. No I/O. No lock held across anything. No HTTP client in scope.
for range ticker.C {
    snap  := store.Load()              // atomic.Pointer load — wait-free
    dirty := compositor.Draw(snap)
    display.Present(dirty)
    health.Pet()                       // watchdog petted ONLY here
}
```

Fetcher goroutines publish by swapping an immutable snapshot pointer. They never touch
the framebuffer; the renderer never performs I/O. A hung API cannot stall a frame,
structurally, and the guarantee is enforced by what gets passed to whom.

The watchdog moving into the render loop is the fix for the boot loop: "frames stopped"
now reboots, "network stalled" does not.

## Failure handling

**Fetch** — every request carries `context.WithTimeout`. Failure is a *state*, not a fatal:
last-known-good data stays on screen with a staleness marker, retried with exponential
backoff + jitter. Per-source last-success and last-error are surfaced on screen and in the
web UI.

**No placeholder data — ever.** A mirror that displays invented values is worse than one
displaying nothing, because it destroys the operator's ability to detect a fault. Absence
is a *rendered state* — dashes, a skeleton, a staleness badge — never a fabricated value.

The type system enforces this rather than discipline. A Go zero-value struct is exactly
the hazard that bit v1, so sources never hand out bare structs:

```go
type Reading[T any] struct {
    Value       T          // meaningless unless Status == Fresh || Status == Stale
    Status      Status     // Never | Fresh | Stale | Failed
    LastSuccess time.Time
    LastErr     error
}
```

`Status == Never` is the boot state and renders as placeholder *chrome*, not placeholder
*data*. A widget cannot accidentally paint a zero value as real, because reaching `Value`
means reading `Status` first.

**WiFi escalation ladder** — each rung has a dwell time; success resets to the top.
Reboot is the last rung, not the first.

```
probe fails (DNS + TCP to known host, every 30s)
  → wpa_cli reassociate
  → restart wpa_supplicant
  → rmmod brcmfmac && modprobe brcmfmac      # hard-reset radio, no reboot
  → reboot
```

**Crash** — BusyBox init respawns in under a second; no reboot. A crash counter in
`/data` drives update rollback (below).

**Wedge** — BCM2835 hardware watchdog remains the floor. A physical hard reset should
never be the answer.

## Image

**Single FAT32 partition, rootfs as a built-in initramfs.** No repartitioning, no second
filesystem:

```
p1  FAT32  (whole card)   bootcode.bin, start.elf, fixup.dat, *.dtb
                          config.txt, cmdline.txt
                          kernel.img        zImage + embedded initramfs (one file)
                          kernel.prev.img   previous kernel, for rollback
                          config.json, wpa_supplicant.conf   (user-editable)
                          mm.current, mm.previous            (Go binary + rollback)
                          health, logs, cache
```

The rootfs is `CONFIG_INITRAMFS_SOURCE`, built into the kernel — so the OS ships as exactly
one file. Three consequences, all good:

- **Nothing to corrupt on power loss.** The rootfs is in RAM; there is no writable OS
  filesystem to fsck. Stronger than a read-only squashfs, which still has to survive a
  torn write.
- **Smaller kernel** — no ext4, no squashfs drivers.
- **The FAT partition is `/data`**, so config lives where it already lives in v1 and stays
  readable from any laptop.

Cost is ~30MB of RAM permanently held for the rootfs, out of 512MB. Noise.

Buildroot: `BR2_EXTERNAL` tree in `board/`, based on `raspberrypi0w_defconfig`.
BusyBox init, musl libc, heavily trimmed kernel (no bluetooth, no USB storage, no
netfilter, no sound). Packages: `wpa_supplicant` (nl80211, no dbus), `hostapd`,
`dnsmasq`, `rpi-firmware`, brcmfmac firmware, and the `magicmirror` package.
`dropbear` behind a debug defconfig fragment.

The Go binary is `CGO_ENABLED=0 GOARCH=arm GOARM=6` — fully static, no libc dependency.
Only wpa_supplicant/hostapd/dnsmasq link musl.

The Go binary lives on the FAT partition as `mm.current`, *not* inside the initramfs — so
app updates never rebuild the kernel, and the two update tiers stay independent.

Rough budget:

| | |
|---|---|
| Pi boot firmware (`bootcode.bin`, `start.elf`, `fixup.dat`, dtb) | ~7MB |
| `kernel.img` — zImage + initramfs (busybox, wpa_supplicant, hostapd, dnsmasq, brcm firmware) | ~10MB |
| `mm.current` — static Go binary, `-ldflags="-s -w"`, fonts + icons embedded | ~9MB |
| **Total** | **~26MB** |

**Target: under 30MB, boot to first frame under 12s.**

Note this is a few MB worse than the read-only-squashfs layout would have been, since the
Go binary sits uncompressed on FAT. That is the deliberate price of a migration that's a
file copy and an update path that doesn't touch the kernel.

## Rendering on ARMv6

1920×1080×4 = 8.3MB per full frame. A full-screen blit alone is ~30ms on a Pi Zero
before any rasterization, so full redraws are not the design:

- Each widget rasterizes into its own offscreen buffer and marks itself dirty only on
  content change.
- The compositor blits dirty widget rects to the mapped framebuffer.
- The common tick — seconds advancing — touches one small rect.
- Glyph masks cached per (rune, size, weight); rasterization on ARMv6 is the real cost,
  not the blit.

Font via `golang.org/x/image/font/opentype` + vector rasterizer, TTF embedded with
`go:embed`. Weather icons are already PNGs in `icons/` — embedded directly, replacing the
17k lines of generated pixel data in `include/ui/weather_icons.h` and
`src/ui/icons/weather_icons.cpp`.

Layout carries over from `widget_base.h`: 12 columns × 16 rows, padding 20, gap 5.

## Migrating from v1 — SD swap, not OTA

**The v1→v2 transition requires physical access. Every update after it is over the air.**

v1's `UpdateService::DownloadAndInstall` (`src/services/update_service.cpp:186-224`) can do
exactly one thing: download a single release asset to `SD:/kernel.new` and rename it over
`kernel.img`. It cannot write `config.txt`, cannot repartition, and verifies nothing beyond
"file is non-empty." Three blockers follow:

1. **`dtoverlay=sdio`** in `config.txt` (`deploy.sh:248`) exists for Circle's own WiFi
   stack. Under Linux the onboard BCM43430 is already described by the stock
   `bcm2708-rpi-zero-w.dtb`, and that overlay points SDIO at different GPIOs than the
   onboard radio uses — expected to break WiFi. Fixing it means writing `config.txt`.
2. **`cmdline.txt` is `console=serial0,115200`.** `serial0` is a device-tree alias, not
   something Linux resolves; it wants `ttyAMA0`. Not fatal to boot, but it means no serial
   output at exactly the moment you'd need it.
3. **No rollback, and this is the fatal one.** Line 211 does `f_unlink(KERNEL_IMG)` *before*
   the rename — the old kernel is deleted first. If the Linux image fails to come up for
   any reason, there is no previous kernel and the device is recoverable only by pulling
   the card.

Shipping a bare-metal→Linux transition down a one-way, unverified, no-rollback channel
would introduce precisely the unrecoverable failure mode this rewrite exists to eliminate.

**Migration is therefore a file copy onto the existing card**, not a reflash — which is why
v2 keeps a single FAT32 partition:

```
mount the card, then:
  replace  kernel.img, config.txt, cmdline.txt
  add      mm.current
  keep     wpa_supplicant.conf, config.json   ← untouched, in place
```

No reformat, no repartition, no re-entering WiFi credentials, and v1's `config/config.json`
is forward-compatible with the v2 schema. `scripts/migrate.sh` does this and takes a backup
of the card's boot files first.

### After that, everything is OTA

Two tiers, both with rollback:

| Tier | Payload | Frequency | Rollback |
|---|---|---|---|
| **App** | `mm.current` (~11MB) | often | keep `mm.previous`; init reverts after 3 failed health markers |
| **OS** | `kernel.img` (~13MB) | rare | keep `kernel.prev.img`; initramfs `/init` reverts and reboots |

OS rollback works on a Pi Zero W without any bootloader support: `/init` runs from RAM
before anything else is mounted, so it can mount the FAT partition, read a boot-attempt
counter, and swap `kernel.prev.img` back over `kernel.img` before rebooting. The
counter is cleared once the app writes its health marker.

Unlike v1, v2's updater verifies a checksum before installing anything, and never deletes
the thing it's replacing.

## Boot presentation

The rainbow test pattern is drawn by the *firmware* (`start.elf`) before any kernel exists,
so it can only be suppressed, never replaced in place. Branding is therefore a matter of
suppressing every stock artefact and owning the screen from the earliest moment we can.

Three stock things to kill, in `config.txt` / `cmdline.txt`:

```
disable_splash=1                    # rainbow test pattern (firmware)
logo.nologo                         # Tux penguin (kernel)
quiet loglevel=0 vt.global_cursor_default=0   # kernel log spew + blinking cursor
```

That leaves a black screen from firmware handoff until the app paints. Filling it:

| Stage | When | What's shown |
|---|---|---|
| Firmware | 0–2s | black (`disable_splash=1`) |
| Kernel fbcon init | ~1s in | **custom `logo_linux_clut224.ppm`** — earliest possible logo |
| App start | ~6–10s | real branded splash, then live boot status |

The kernel logo is `CONFIG_LOGO` with our own 224-colour PPM. It's the earliest a logo can
appear, at the cost of a limited palette and top-left placement (centering needs a patch —
not worth it; design the mark to sit well in the corner, or skip this stage and accept
black).

The app's splash is just frame 0 of the normal renderer — same framebuffer, no handoff
seam, no separate splash program. And per the no-placeholder-data rule, **it transitions
into live boot status rather than sitting there looking finished**: "connecting to
Wi-Fi…", "fetching weather…", the IP address once associated. A mirror that shows a
polished logo while silently failing to reach the network is the same bug as the Dallas
weather.

(v1 had `src/ui/loading_screen.cpp` for roughly this, but it was never in the Makefile's
`OBJS` — the boot status was dead code and never displayed.)

Assets live in `assets/` — logo as SVG source, with the PPM and the app-splash raster
generated at build time so there's one source of truth. The mark itself gets drafted at
milestone 2, alongside the framebuffer bring-up that first needs it.

## Widgets are pluggable, and describe their own config

Slots are configurable: the bottom-left tile might be the upcoming-events list today and
something else tomorrow, and that something else has its own settings. v1 anticipated this
— `include/config/config.h` declares a `WidgetConfig` array with `type`, `id` and
`position` — but never wired it up; `kernel.cpp` instantiates four widgets as stack
objects. This makes it real.

**Each widget type self-describes.** The web UI must not need a hand-written form per
widget, or adding a widget becomes a two-language change and stops happening.

```go
type Descriptor struct {
    Type        string        // "upcoming_events"
    Name        string        // "Upcoming Events"
    Description string
    DefaultSpan layout.Span
    MinSpan     layout.Span
    Fields      []Field       // drives config UI + validation
    Needs       []SourceKind  // e.g. SourceCalendar — for fetcher dedup
    New         func(json.RawMessage) (Widget, error)
}

type Field struct {
    Key      string
    Label    string
    Type     FieldType   // Text | Number | Bool | Select | Color | URL | Duration | ListOf
    Default  any
    Options  []Option    // Select only
    Min, Max *float64
    Help     string
    Required bool
}
```

Widgets `Register()` themselves in `init()`. `GET /api/widget-types` returns the
descriptors; the web UI renders every config form generically from `Fields`. **Adding a
widget is one Go file and zero web changes.**

Config carries widget settings opaquely:

```json
{
  "timezone": "America/Chicago",
  "layout": { "cols": 12, "rows": 16, "padding": 20, "gap": 5 },
  "widgets": [
    { "id": "events-1", "type": "upcoming_events",
      "pos": { "col": 0, "row": 11, "colSpan": 4, "rowSpan": 5 },
      "config": { "maxEvents": 6, "horizonDays": 14, "showLocation": true } }
  ]
}
```

The `config` object is `json.RawMessage` — the core loader never learns widget-specific
fields. Generic validation runs against `Fields` first; the widget's `New` does any deeper
checks.

**Sources are shared, not per-widget.** Widgets declare `Needs`; the source manager runs
one fetcher per unique source config. Two calendar widgets don't double-fetch your ICS
feeds, and swapping a widget out drops its fetcher only if nothing else needs it.

So feed definitions stay top-level, as they already are in v1's config — calendar URLs,
names and colours live under `calendars`, and widgets reference them by id:

```json
"calendars": [ { "id": "work", "url": "https://...", "name": "Work", "color": "#FA5258" } ],
"widgets":   [ { "id": "events-1", "type": "upcoming_events",
                 "config": { "feeds": ["work", "home"], "maxEvents": 6 } } ]
```

Both the month grid and the events list consume the same feeds today; duplicating URLs into
each widget's config would mean editing a URL in two places and fetching it twice.

### A bad widget config must not brick the mirror

Same fault-isolation rule as everything else:

- A widget that fails to construct renders an error tile in its slot. Other widgets are
  unaffected.
- Each widget's render is wrapped in `recover()`; a panicking widget becomes an error tile
  rather than taking down the render loop.
- An unknown `type` — a config written by a newer binary, then rolled back — renders a
  placeholder naming the missing type. It is never a failed boot.
- **Config gets the same known-good treatment as binaries.** A config that fails to load or
  validate leaves the previous one running, with the error surfaced in the web UI. You
  cannot lock yourself out of the mirror by saving a bad form.

Config changes apply live: the widget tree is rebuilt and swapped atomically, the same
pointer-swap pattern the data snapshots use. No restart, no reboot.

Deferred deliberately: drag-and-drop grid editing. The first web UI edits position as
numbers with a live grid preview. The registry is what has to exist early; the editor can
get nicer any time.

## Repo layout

```
cmd/magicmirror/          wiring, supervision, signals
internal/display/         fbdev mmap, format detect, double buffer, dirty rects
                          + PNG backend for host development
internal/render/          text, blit, shapes, glyph cache
internal/layout/          12×16 grid
internal/widget/          datetime, weather, forecast, calendar, upcoming
internal/store/           atomic snapshot, per-source freshness + last error
internal/source/          open-meteo, weather.gov, ics, github releases
internal/netmon/          wifi supervisor + escalation ladder
internal/provision/       hostapd/dnsmasq lifecycle, captive portal
internal/config/          schema, atomic load/save, validation
internal/update/          poll, verify, swap, health marker, rollback
internal/health/          watchdog, crash counter
web/                      embedded config UI (html/template + vanilla JS, no build step)
board/                    BR2_EXTERNAL: defconfig, package/, overlay, post-image
icons/                    carried over unchanged
```

Kept from v1: `icons/`, `config/config.json.example` (schema), `scripts/release.sh`.
Deleted: `src/`, `include/`, `lib/`, `deps/`, `init.sh`, `Makefile`, `deploy.sh`, submodules.

## Development story

The single highest-value change. `internal/display` has two backends behind one interface:
`/dev/fb0` on device, PNG on the host. The same widget code runs both ways.

- `go test ./...` covers the ICS parser, RRULE expansion, WMO code mapping, layout math.
- Golden-PNG tests catch layout regressions.
- `go run ./cmd/magicmirror -backend=png` iterates layout in seconds, not an SD flash.
- Fault injection is a test target: a tarpit HTTP server that accepts and never responds
  is the regression test for the freeze bug, and it runs on the host.

## Milestones

Each one ends at a bootable card.

1. **Buildroot boots to a shell.** Nothing else. Proves toolchain, kernel, SD layout.
2. **Hello-pixel.** Static ARMv6 Go binary, init-respawned, gradient on `/dev/fb0`.
   Proves cross-compile, framebuffer format, respawn. Rainbow, penguin and kernel log
   suppressed here — boot is black-to-app from this milestone on, and never regresses.
3. **Clock.** Font rendering, glyph cache, grid, dirty rects — and the widget registry,
   with datetime as its first consumer. Built against the registry from here on; nothing
   is retrofitted later.
4. **Network.** wpa_supplicant, DHCP, IP on screen, escalation ladder.
   *Acceptance: pull the AP's power; device recovers without reboot.*
5. **Data.** Weather, forecast, ICS behind the snapshot store.
   *Acceptance: point a source at a tarpit; clock keeps ticking, no reboot.* ← the bug
6. **Widgets.** Remaining layout ported to parity with v0.14.0.
7. **Config web UI.** Generic form rendering from widget descriptors; add, remove, retype
   and reposition widgets live. *Acceptance: save a deliberately invalid widget config;
   the mirror keeps running on the previous one and shows the error in the UI.*
8. **AP provisioning portal.**
9. **Update + rollback**, both tiers, plus `scripts/migrate.sh`.
   *Acceptance: ship a deliberately broken binary; device reverts. Ship a deliberately
   broken kernel; initramfs `/init` reverts it.*
10. **Size and boot-time pass.**

Milestones 4, 5 and 9 have adversarial acceptance criteria on purpose — those are the three
symptoms, and each is a test that fails today.
