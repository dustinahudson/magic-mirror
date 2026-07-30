# Open questions

Things I decided on my own to keep moving, plus things I genuinely can't
answer without you. Nothing here is blocking — each has a working default in
the code and a note on how to change it.

Grouped by how much I'd like your input.

---

## 1. Worth your opinion — design calls I made blind

### 1.1 Weather icons are in colour

The `assets/icons/*.png` carried over from v1 are full colour: blue
raindrops, a yellow sun, a pale blue moon. They look good on a monitor.

**On an actual mirror behind half-silvered glass I don't know if they do.**
Colour transmits differently than white through that coating, and saturated
blue at low luminance is the worst case. If they read badly in the real
build, `render.DrawImageTinted` already exists — swapping to flat white is a
one-line change per widget.

**Decided for now:** keep them in colour, since that's what v1 did.
**Want to know:** do they actually look right on your glass?

### 1.2 Clock size

v1 rendered the time at 48px (`lv_font_montserrat_48`) on the same 1080p
panel. I defaulted to **96px**, because a mirror is read from across a room
and 48px felt undersized in the frames I looked at.

It's the `timeSize` field on the clock widget, adjustable from the web UI
once that lands. Same question for weather's `tempSize` (I chose 88px).

### 1.3 Default widget arrangement

I kept v1's 12×16 grid geometry exactly (`widget_base.h`: 12 cols, 16 rows,
20px padding, 5px gap), but **the arrangement is mine** — v1 never had one in
config, `kernel.cpp` just instantiated four widgets as stack objects, so
there was nothing to copy.

Current default: clock top-left, weather + forecast top-right, calendar
bottom-left, events bottom-right, status bar across the foot.

If you want the v1 positions reproduced exactly, I'd need a photo or the
LVGL flex parameters — tell me and I'll match it.

### 1.4 Forecast starts tomorrow, not today

The forecast row shows the next 5 days *excluding* today, since today is
already the big number in the weather widget. v1 showed 5 days from
`forecast_days=5` starting at today, so its first column duplicated current
conditions.

**Decided:** skip today. Easy to revert (`weather.go`, the `for i := 1;`
loop).

### 1.5 Font is Inter, not Montserrat

v1 used LVGL's bundled Montserrat. I used **Inter** (SIL OFL, three weights,
~1.2MB embedded) because it was designed for screen UI at small sizes and has
better tabular figures — which matters for a clock, where proportional digits
make the seconds jitter.

If you want Montserrat back for visual continuity, it's a file swap in
`assets/fonts/` plus the constants in `assets/assets.go`.

---

## 2. Flagging a security decision

### 2.1 The live preview server is unauthenticated

`-preview :8080` serves the mirror's framebuffer over HTTP with no auth. On
your laptop that's exactly right. **On the device it would expose your
calendar and your home address's weather to anyone on the LAN**, as a live
image feed.

**Decided:** the preview is opt-in via flag and is *not* enabled in the
device's default init script. It stays a development tool.

If you want it on the device for debugging, I'd suggest binding it to
localhost and reaching it over an SSH tunnel rather than opening it to the
network. Say the word and I'll wire that up.

### 2.2 Calendar URLs are redacted in logs, but not in config

Google Calendar "secret address" ICS links are credentials. `source/calendar.go`
strips the query string before anything reaches a log or the screen.

But they sit in plaintext in `config.json` on a **FAT partition with no
permissions**, readable by anyone who pulls the SD card. That's also true of
v1, and I don't think it's worth encrypting — but you should know it's the
case, because it means a lost SD card is a leaked calendar.

---

## 3. Decided, just telling you

### 3.1 Timezone data is embedded in the binary

Go's `time/tzdata` embeds the IANA database (~450KB) so `time.LoadLocation`
works without system tzdata in the rootfs. Shipping tzdata as a Buildroot
package would be 1-3MB depending on trimming, so **embedding is both smaller
and one less thing that can be missing at runtime**.

Matters for ICS `TZID` resolution, which would otherwise silently fall back
to the default zone for every non-UTC event.

### 3.2 api.weather.gov was dropped

v1 called Open-Meteo *and* `api.weather.gov` for a short text description
(`weather_service.cpp:118`), with a comment noting weather.gov "is US-only
and can be flaky".

One Open-Meteo request now returns current conditions *and* the forecast, and
WMO codes map to descriptions locally (`WMODescription`). That's one round
trip instead of three, no US-only dependency, and one fewer flaky service.

### 3.3 Event list keeps in-progress events

An event that started an hour ago and runs another hour stays in the list
until it *ends*, rather than disappearing at its start time. Seemed obviously
right; mentioning it because it's a behaviour change from v1, which compared
against start time.

### 3.4 Truncation is reported rather than silent

v1 used fixed arrays (`static mm::CalendarEvent refreshEvents[200]`) and
silently dropped anything beyond. Now the cap is explicit and the status bar
says "event list truncated" when it bites.

---

## 4. Needs a decision before the relevant milestone

### 4.1 Release asset naming (blocks M9)

The updater needs to know which asset on a GitHub release is the binary.
I'm planning to look for an asset named exactly `magicmirror-armv6`, with
`kernel.img` as the OS-tier asset, and to verify a `SHA256SUMS` asset before
installing anything.

Tell me if you'd rather it matched on a pattern or used a different naming
scheme, otherwise I'll build that.

### 4.2 AP portal SSID and password (blocks M8)

Planning: SSID `MagicMirror-Setup`, **open network** (no password), portal at
`http://192.168.4.1`, only active when no known network can be joined.

Open is the friendlier choice for a setup flow you reach from a phone, and
the window is short. But it does mean that during setup, anyone in range can
connect and see the portal. A fixed WPA2 password would be printed... where?
There's no sticker on a DIY build. **Leaning open — say if you'd rather have
a password.**

### 4.3 Web config UI has no authentication (relates to M7)

Same shape as 2.1: the config page on port 80 lets anyone on your LAN change
your mirror's settings and read your calendar URLs.

Options are none (simplest, matches most home appliances), a shared password
in config, or bind-to-localhost-plus-SSH. **Leaning none for v1 of the web UI,
with a note in the README** — but this is your network, so your call.

---

## 5. Deferred deliberately

- **Drag-and-drop grid editing.** The first web UI edits position as numbers
  with a live grid preview. The registry is what had to exist early.
- **Font subsetting.** 1.2MB of Inter is embedded whole. Subsetting to the
  glyphs actually used would save perhaps 1MB. Deferred to M10, where it
  belongs with the rest of the size work.
- **Moon phase icons.** `assets/icons/` has eight moon phase PNGs that
  nothing currently uses; v1 had them for a widget it never shipped. Worth a
  `moon` widget later if you want one.

---

## 6. Status

Everything is implemented and running on hardware. The mirror is up, on the
network, fetching real weather and calendar data, reachable at
`magicmirror.local` over SSH and HTTP, and deployable without a card.

| M | What | State |
|---|---|---|
| 1 | Buildroot image | Boots. 23MB. |
| 2 | Display + preview | Working. fbdev detected 1920x1080 32bpp, fast path. |
| 3 | Render, layout, registry | Working. 61 frames/minute, steady. |
| 4 | WiFi recovery ladder | Working; reboot rung gated on having ever connected. |
| 5 | Store + sources | Working. All sources fresh on device. |
| 6 | Widgets | Working. |
| 7 | Config web UI | Working, plus /api/logs and /api/status. |
| 8 | AP setup portal | AP starts, scans, serves the page. Credential save works. |
| 9 | Update + rollback | Working; dev builds and older tags refused. |
| 10 | Size / boot time | 23MB. Boot-to-frame still unmeasured. |

### Bring-up, and what it cost

Eleven card writes. Every failure was self-inflicted, and every one hid
behind a symptom several layers away from its cause:

- `gpu_mem=16` selected the display-less firmware. No HDMI signal at all.
- The prune deleted `cfg80211`, so `brcmfmac` could not load. Presented as
  DNS failures, a reboot loop, and a portal that could not bind.
- My `inittab` dropped every filesystem mount. `/sys` missing meant every
  interface check failed while the radio was up and running firmware.
- No RTC and no NTP, so the clock sat at 1970 and every TLS handshake
  failed certificate validation. netmon reported the link healthy
  throughout, correctly — its probe is raw TCP with no certificate.
- FAT corruption truncated `kernel.img` to zero bytes. `cp` succeeded and
  `ls` showed 11MB, because both read the page cache rather than flash.
- Three separate SSH bugs stacked: a host key on FAT that could not hold
  `0600`, lazy key generation that only failed on connect, and
  `/etc/dropbear` being a dangling symlink that made `mkdir -p` a silent
  no-op.

The pattern worth keeping: **replacing something Buildroot provides means
inheriting responsibility for everything it did.** The inittab, the
dropbear init script and the module set all broke this way.

The other lesson is about diagnostics. Progress was gated on what the
device could say, not on how hard the bugs were. `/api/logs` should have
existed before any of the features that needed debugging — it resolved the
last two failures in one cycle each.

### Still open

- **Boot-to-first-frame is unmeasured.** Needs a stopwatch, not a tool.
- **The AP portal has not been driven to completion by a phone.** The AP
  comes up, scans, and serves the page; nobody has selected a network on it
  and watched the mirror reconnect.
- **OS-tier rollback is untested.** `kernel.prev.img` is kept, but nothing
  restores it automatically after a failed boot; the initramfs boot-counter
  described in DESIGN.md is not implemented.
- **No golden-PNG layout tests**, though the PNG backend exists.
- **The layout pass has not started.** This was the original next step and
  is still where the interesting work is.
