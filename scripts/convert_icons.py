#!/usr/bin/env python3
"""Convert PNG icons in ./icons/ to LVGL RGB565 C arrays.

Surgically updates src/ui/icons/weather_icons.{cpp,h}:
  - Weather icons (10): replaces existing data blocks at 56x56 (large) and 28x28 (small).
  - Moon phase icons (8): inserts/replaces data blocks at 32x32.
  - Sunset + wind icons in the existing cpp are left untouched (no source PNGs).

Each PNG is cropped to its content bbox, then scaled to fit the target square
preserving aspect ratio, centered on a transparent canvas.
"""
import re
from pathlib import Path
from PIL import Image

REPO = Path(__file__).resolve().parent.parent
ICONS_DIR = REPO / "icons"
CPP_PATH = REPO / "src/ui/icons/weather_icons.cpp"
H_PATH = REPO / "src/ui/icons/weather_icons.h"

WEATHER_ICONS = [
    "clear_day",
    "clear_night",
    "partly_cloudy_day",
    "partly_cloudy_night",
    "cloudy",
    "fog",
    "drizzle",
    "rain",
    "snow",
    "thunderstorm",
]

# Moon phases in standard index order (matches get_moon_icon switch).
MOON_PHASES = [
    "new",
    "waxing_crescent",
    "first_quarter",
    "waxing_gibbous",
    "full",
    "waning_gibbous",
    "last_quarter",
    "waning_crescent",
]

WEATHER_LARGE = 56
WEATHER_SMALL = 28
MOON_SIZE = 32


def rgb_to_rgb565(r, g, b):
    return ((r >> 3) << 11) | ((g >> 2) << 5) | (b >> 3)


def convert(png_path, size):
    """Load PNG, crop to content, scale to fit size x size (aspect-preserving),
    center on transparent canvas, and return RGB565 little-endian byte list."""
    img = Image.open(png_path).convert("RGBA")
    bbox = img.getbbox()
    if bbox:
        img = img.crop(bbox)
    scale = size / max(img.size)
    new_size = (max(1, round(img.size[0] * scale)), max(1, round(img.size[1] * scale)))
    img = img.resize(new_size, Image.Resampling.LANCZOS)
    canvas = Image.new("RGBA", (size, size), (0, 0, 0, 0))
    canvas.paste(img, ((size - new_size[0]) // 2, (size - new_size[1]) // 2), img)

    out = []
    for r, g, b, a in canvas.getdata():
        if a < 255:
            r = (r * a) // 255
            g = (g * a) // 255
            b = (b * a) // 255
        v = rgb_to_rgb565(r, g, b)
        out.append(f"0x{v & 0xff:02x}")
        out.append(f"0x{(v >> 8) & 0xff:02x}")
    return out


def make_block(var, size, pixels, trailing_newlines=1):
    """Produce `static uint8_t <var>_data[] = {...};` followed by the descriptor."""
    lines = [f"static const uint8_t {var}_data[] = {{"]
    for i in range(0, len(pixels), 32):
        lines.append("    " + ", ".join(pixels[i : i + 32]) + ",")
    lines.append("};")
    lines.append("")
    lines.append(f"const lv_image_dsc_t {var} = {{")
    lines.append("    .header = {")
    lines.append("        .magic = LV_IMAGE_HEADER_MAGIC,")
    lines.append("        .cf = LV_COLOR_FORMAT_RGB565,")
    lines.append("        .flags = 0,")
    lines.append(f"        .w = {size},")
    lines.append(f"        .h = {size},")
    lines.append(f"        .stride = {size * 2},")
    lines.append("        .reserved_2 = 0,")
    lines.append("    },")
    lines.append(f"    .data_size = sizeof({var}_data),")
    lines.append(f"    .data = {var}_data,")
    lines.append("};")
    return "\n".join(lines) + "\n" * trailing_newlines


def replace_block(src, var, new_block):
    """Replace the [data array + descriptor] pair for `var` in cpp source."""
    pattern = re.compile(
        r"static const uint8_t " + re.escape(var) + r"_data\[\] = \{.*?"
        r"const lv_image_dsc_t " + re.escape(var) + r" = \{.*?\n\};\n",
        re.DOTALL,
    )
    new_src, n = pattern.subn(new_block, src)
    if n != 1:
        raise RuntimeError(f"Expected 1 match for {var}, got {n}")
    return new_src


def strip_block(src, var):
    """Remove an existing [data + descriptor] block with trailing blank line, if present."""
    pattern = re.compile(
        r"static const uint8_t " + re.escape(var) + r"_data\[\] = \{.*?"
        r"const lv_image_dsc_t " + re.escape(var) + r" = \{.*?\n\};\n\n?",
        re.DOTALL,
    )
    return pattern.sub("", src)


MOON_LOOKUP = """
const lv_image_dsc_t* get_moon_icon(int phase)
{
    switch (phase) {
        case 0: return &icon_moon_new;
        case 1: return &icon_moon_waxing_crescent;
        case 2: return &icon_moon_first_quarter;
        case 3: return &icon_moon_waxing_gibbous;
        case 4: return &icon_moon_full;
        case 5: return &icon_moon_waning_gibbous;
        case 6: return &icon_moon_last_quarter;
        case 7: return &icon_moon_waning_crescent;
        default: return &icon_moon_new;
    }
}
"""


def update_cpp():
    src = CPP_PATH.read_text()

    # --- Weather icons: replace in place (large + small) ---
    for name in WEATHER_ICONS:
        png = ICONS_DIR / f"{name}.png"
        if not png.exists():
            print(f"  SKIP (missing): {png.name}")
            continue
        print(f"  weather: {png.name}")
        for suffix, size in (("", WEATHER_LARGE), ("_small", WEATHER_SMALL)):
            var = f"weather_icon_{name}{suffix}"
            pixels = convert(png, size)
            src = replace_block(src, var, make_block(var, size, pixels))

    # --- Moon icons: strip any existing, then insert before get_weather_icon ---
    moon_blocks = ""
    for phase in MOON_PHASES:
        png = ICONS_DIR / f"moon_{phase}.png"
        if not png.exists():
            print(f"  SKIP (missing): {png.name}")
            continue
        print(f"  moon: {png.name}")
        var = f"icon_moon_{phase}"
        src = strip_block(src, var)
        pixels = convert(png, MOON_SIZE)
        moon_blocks += make_block(var, MOON_SIZE, pixels, trailing_newlines=2)

    # Remove any prior get_moon_icon lookup before re-inserting.
    src = re.sub(
        r"\nconst lv_image_dsc_t\* get_moon_icon\(int phase\)\s*\{.*?\n\}\n",
        "",
        src,
        flags=re.DOTALL,
    )

    anchor = "const lv_image_dsc_t* get_weather_icon("
    idx = src.index(anchor)
    src = src[:idx] + moon_blocks + src[idx:]
    src = src.rstrip() + "\n" + MOON_LOOKUP

    CPP_PATH.write_text(src)
    print(f"Wrote: {CPP_PATH}")


def update_header():
    h = H_PATH.read_text()

    moon_externs = "\n".join(
        f"extern const lv_image_dsc_t icon_moon_{p};" for p in MOON_PHASES
    ) + "\n"

    if "icon_moon_new" not in h:
        wind_anchor = "extern const lv_image_dsc_t icon_wind_12;\n"
        if wind_anchor not in h:
            raise RuntimeError("Expected icon_wind_12 extern in header")
        h = h.replace(wind_anchor, wind_anchor + moon_externs)

        proto_anchor = "const lv_image_dsc_t* get_wind_icon(int wind_speed_mph);\n"
        if "get_moon_icon" not in h:
            h = h.replace(
                proto_anchor,
                proto_anchor + "const lv_image_dsc_t* get_moon_icon(int phase);\n",
            )
        H_PATH.write_text(h)
        print(f"Wrote: {H_PATH}")
    else:
        print(f"{H_PATH.name} already has moon declarations")


def main():
    update_cpp()
    update_header()
    print("Done.")


if __name__ == "__main__":
    main()
