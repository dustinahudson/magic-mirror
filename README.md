# Magic Mirror

A smart mirror for the Raspberry Pi Zero W: a minimal Buildroot Linux image
that boots straight into a single static Go binary, renders to the
framebuffer, and serves a configuration page over the network.

The v1 build (bare-metal C++ on [Circle OS](https://github.com/rsta2/circle))
is preserved at tag `v0.14.0`. See [DESIGN.md](DESIGN.md) for why it was
replaced and how this one is put together.

## What it shows

Clock, current weather, multi-day forecast, a month calendar, and an upcoming
events list drawn from any number of iCalendar feeds. Widgets are pluggable
and positioned on a 12×16 grid — see [Widgets](#widgets).

## Why it works this way

Three field failures drove the rewrite, and they were all one bug: the render
loop and the network fetch were the same thread of control.

| Symptom | Now |
|---|---|
| Screen froze while refreshing calendars | The renderer holds no HTTP client and reads data through a single atomic pointer load. A hung API cannot stall a frame. |
| A dead API caused a boot loop | Every fetch has a deadline; failure is a state, not a fatal. The watchdog is petted only from the render loop, so a stalled network is no longer indistinguishable from a wedged device. |
| A network drop needed a hard reset | Recovery escalates — reassociate, restart the supplicant, reload the driver — with reboot as the *last* rung rather than the first. |

There is a regression test for the first two:
`internal/mirror/freeze_test.go` points the sources at servers that accept
connections and never answer, then asserts every frame still paints.

**The mirror never displays invented data.** v1 seeded a complete fake
weather record, so a mirror that had never reached the weather API looked
identical to a working one. Here, absence renders as em dashes and a
staleness marker; a value on screen is always a real measurement.

## Running it without a Pi

The renderer has two backends behind one interface, so the same widget code
that writes `/dev/fb0` on the device serves a live preview on your laptop:

```sh
cp config.json.example config.json   # edit it
make run                             # then open http://localhost:8080
```

The preview page updates the instant a frame is presented. Layout iteration
is a page refresh rather than an SD card flash.

```sh
make test     # unit tests: ICS parsing, recurrence, recovery ladder, updates
make host     # host binary
make binary   # static ARMv6 binary for the device
```

## Building the image

```sh
make image    # fetches Buildroot, builds toolchain + kernel + initramfs
```

Expect an hour or more on a first run. Output lands in
`board/buildroot/output/images/boot/` as the contents of a single FAT32
partition.

Roughly 26MB total: ~7MB Pi firmware, ~10MB `kernel.img` (Linux with the
rootfs linked in as an initramfs), ~9MB `mm.current`.

## Installing

### Migrating an existing v1 card

A file copy onto the card you already have — no reformat, no repartition, and
your WiFi credentials stay in place:

```sh
scripts/migrate.sh --dry-run /media/$USER/BOOT   # see what it would do
scripts/migrate.sh /media/$USER/BOOT
```

It backs up the v1 boot files to `v1-backup/` first and prints the command to
restore them.

**This cannot be done over the air.** v1's updater can only replace a single
file named `kernel.img`, cannot rewrite `config.txt` (which still carries a
`dtoverlay=sdio` line that breaks WiFi under Linux), and deletes the old
kernel before installing the new one — so a failed boot would leave nothing
to fall back to.

### A fresh card

Format a card as FAT32 and copy the contents of
`board/buildroot/output/images/boot/` onto it.

### First boot

Expect about ten seconds of black screen — that is Linux booting with the
rainbow, the penguin and the console output all suppressed — then the mirror.

To set up WiFi, either:

- Put a `wpa_supplicant.conf` on the card before booting, or
- Let it boot without one. It starts an open access point called
  **MagicMirror-Setup**; connect from a phone and a setup page opens where
  you pick a network.

The status bar shows the IP address once connected.

## Configuring

Point a browser at the mirror's IP address. The page covers timezone, units,
weather location, calendar feeds, and every widget's position and settings.

Two things worth knowing:

- **You cannot lock yourself out.** A config that fails validation is
  rejected before it reaches the disk or the render loop, and the mirror
  carries on with what it had.
- **There is no authentication.** Anyone on your network can read your
  calendar URLs and change your settings. That matches most home appliances,
  but it is a deliberate choice — see [QUESTIONS.md](QUESTIONS.md).

Changes apply live; no restart.

## Widgets

Each widget type declares its own configuration fields, and the web UI
generates the form from that declaration. **Adding a widget type is one Go
file and no changes to the web UI.**

```go
func init() {
    Register(Descriptor{
        Type:        "moon",
        Name:        "Moon Phase",
        DefaultSpan: layout.Span{Cols: 3, Rows: 3},
        Fields: []Field{
            {Key: "showLabel", Label: "Show phase name", Type: FieldBool, Default: true},
        },
        New: newMoon,
    })
}
```

Calendar feeds are defined once at the top level and referenced by widgets by
id, so a URL lives in one place and is fetched once however many widgets show
it.

A widget that fails to build, panics while rendering, or names a type this
binary does not know becomes a labelled placeholder in its own slot. It never
takes down the mirror.

## Updates

Two independent tiers, both with rollback:

| Tier | Artifact | Rollback |
|---|---|---|
| App | `mm.current` | `mm.previous` is restored after three starts that never reach healthy |
| OS | `kernel.img` | `kernel.prev.img` is kept |

Both verify a published `SHA256SUMS` before installing anything, and neither
ever deletes what it is replacing.

App updates exit cleanly and init respawns the new binary — no reboot.

## Layout

```
cmd/magicmirror/     entry point and the render loop
internal/display/    fbdev, live HTTP preview, PNG output
internal/render/     glyph-cached text, shapes, icon scaling
internal/layout/     12×16 grid
internal/widget/     the registry and the widget implementations
internal/store/      atomic snapshots with explicit data status
internal/source/     weather, calendar, system fetchers
internal/ics/        iCalendar parsing and recurrence expansion
internal/netmon/     WiFi recovery ladder
internal/provision/  AP setup portal
internal/update/     two-tier self-update
internal/web/        config UI
internal/health/     watchdog and health marker
board/               Buildroot external tree, init scripts, boot files
assets/              embedded fonts and weather icons
```

## Hardware

- Raspberry Pi Zero W
- HDMI display (1920×1080)
- MicroSD card (any size; the image is ~26MB)
- A two-way mirror, if you want it to be a mirror

## Licence

MIT. Inter is licensed under the SIL Open Font License; see
`assets/fonts/LICENSE-Inter.txt`.
