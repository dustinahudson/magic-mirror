#!/usr/bin/env python3
"""
Convert PNG weather icons to LVGL v9 C arrays for bare-metal use.
"""

import sys
from pathlib import Path

try:
    from PIL import Image
except ImportError:
    print("Please install Pillow: pip install Pillow")
    sys.exit(1)

ICON_SIZE_LARGE = 56
ICON_SIZE_SMALL = 28
ICON_SIZE_INFO = 32  # For wind/sun info line icons
ICONS_DIR = Path(__file__).parent.parent / "assets/icons/weather-icons/production/fill/png"
OUTPUT_DIR = Path(__file__).parent.parent / "src/ui/icons"

WEATHER_ICONS = {
    "clear_day": "clear-day.png",
    "clear_night": "clear-night.png",
    "partly_cloudy_day": "partly-cloudy-day.png",
    "partly_cloudy_night": "partly-cloudy-night.png",
    "cloudy": "cloudy.png",
    "fog": "fog.png",
    "drizzle": "drizzle.png",
    "rain": "rain.png",
    "snow": "snow.png",
    "thunderstorm": "thunderstorms.png",
}

# Additional icons (only tiny size needed)
INFO_ICONS = {
    "sunset": "sunset.png",
}

# Wind Beaufort scale icons (0-12)
WIND_ICONS = {f"wind_{i}": f"wind-beaufort-{i}.png" for i in range(13)}

def rgb_to_rgb565(r, g, b):
    return ((r >> 3) << 11) | ((g >> 2) << 5) | (b >> 3)

def convert_png_to_lvgl(png_path, size):
    """Convert PNG to LVGL RGB565 format (no alpha for simplicity)."""
    img = Image.open(png_path)
    img = img.resize((size, size), Image.Resampling.LANCZOS)
    
    if img.mode != 'RGBA':
        img = img.convert('RGBA')
    
    pixels = list(img.getdata())
    width, height = img.size
    
    # Use RGB565 format (2 bytes per pixel, little endian)
    c_array = []
    for pixel in pixels:
        r, g, b, a = pixel
        # Pre-multiply alpha and blend with black background for transparency
        if a < 255:
            r = (r * a) // 255
            g = (g * a) // 255
            b = (b * a) // 255
        rgb565 = rgb_to_rgb565(r, g, b)
        c_array.append(f"0x{rgb565 & 0xff:02x}")
        c_array.append(f"0x{(rgb565 >> 8) & 0xff:02x}")
    
    stride = width * 2  # RGB565 = 2 bytes per pixel
    return width, height, stride, c_array

def main():
    OUTPUT_DIR.mkdir(parents=True, exist_ok=True)
    
    print("Converting weather icons to LVGL v9 format...")
    
    # Generate header
    header = '''#ifndef WEATHER_ICONS_H
#define WEATHER_ICONS_H

#include "lvgl.h"

#ifdef __cplusplus
extern "C" {
#endif

'''
    for name in WEATHER_ICONS.keys():
        header += f"extern const lv_image_dsc_t weather_icon_{name};\n"
        header += f"extern const lv_image_dsc_t weather_icon_{name}_small;\n"

    # Info icons (tiny size only)
    for name in INFO_ICONS.keys():
        header += f"extern const lv_image_dsc_t icon_{name};\n"

    # Wind icons
    for name in WIND_ICONS.keys():
        header += f"extern const lv_image_dsc_t icon_{name};\n"

    header += '''
const lv_image_dsc_t* get_weather_icon(int wmo_code, bool is_day, bool small_size);
const lv_image_dsc_t* get_wind_icon(int wind_speed_mph);

#ifdef __cplusplus
}
#endif

#endif
'''
    
    with open(OUTPUT_DIR / "weather_icons.h", 'w') as f:
        f.write(header)
    print(f"Generated: weather_icons.h")
    
    # Generate source
    source = '''#include "weather_icons.h"

'''
    
    for name, filename in WEATHER_ICONS.items():
        for src_size in [64, 128, 256]:
            src_path = ICONS_DIR / str(src_size) / filename
            if src_path.exists():
                break
        else:
            print(f"Warning: Icon not found: {filename}")
            continue
        
        print(f"  Converting: {filename}")
        
        # Large version
        width, height, stride, pixels = convert_png_to_lvgl(src_path, ICON_SIZE_LARGE)
        source += f"static const uint8_t weather_icon_{name}_data[] = {{\n"
        for i in range(0, len(pixels), 32):
            source += "    " + ", ".join(pixels[i:i+32]) + ",\n"
        source += "};\n\n"
        
        source += f'''const lv_image_dsc_t weather_icon_{name} = {{
    .header = {{
        .magic = LV_IMAGE_HEADER_MAGIC,
        .cf = LV_COLOR_FORMAT_RGB565,
        .flags = 0,
        .w = {width},
        .h = {height},
        .stride = {stride},
        .reserved_2 = 0,
    }},
    .data_size = sizeof(weather_icon_{name}_data),
    .data = weather_icon_{name}_data,
}};

'''
        
        # Small version
        width, height, stride, pixels = convert_png_to_lvgl(src_path, ICON_SIZE_SMALL)
        source += f"static const uint8_t weather_icon_{name}_small_data[] = {{\n"
        for i in range(0, len(pixels), 32):
            source += "    " + ", ".join(pixels[i:i+32]) + ",\n"
        source += "};\n\n"
        
        source += f'''const lv_image_dsc_t weather_icon_{name}_small = {{
    .header = {{
        .magic = LV_IMAGE_HEADER_MAGIC,
        .cf = LV_COLOR_FORMAT_RGB565,
        .flags = 0,
        .w = {width},
        .h = {height},
        .stride = {stride},
        .reserved_2 = 0,
    }},
    .data_size = sizeof(weather_icon_{name}_small_data),
    .data = weather_icon_{name}_small_data,
}};

'''
    
    # Generate info icons (tiny size)
    for name, filename in INFO_ICONS.items():
        for src_size in [64, 128, 256]:
            src_path = ICONS_DIR / str(src_size) / filename
            if src_path.exists():
                break
        else:
            print(f"Warning: Icon not found: {filename}")
            continue

        print(f"  Converting: {filename} (tiny)")

        width, height, stride, pixels = convert_png_to_lvgl(src_path, ICON_SIZE_INFO)
        source += f"static const uint8_t icon_{name}_data[] = {{\n"
        for i in range(0, len(pixels), 32):
            source += "    " + ", ".join(pixels[i:i+32]) + ",\n"
        source += "};\n\n"

        source += f'''const lv_image_dsc_t icon_{name} = {{
    .header = {{
        .magic = LV_IMAGE_HEADER_MAGIC,
        .cf = LV_COLOR_FORMAT_RGB565,
        .flags = 0,
        .w = {width},
        .h = {height},
        .stride = {stride},
        .reserved_2 = 0,
    }},
    .data_size = sizeof(icon_{name}_data),
    .data = icon_{name}_data,
}};

'''

    # Generate wind icons (tiny size)
    for name, filename in WIND_ICONS.items():
        for src_size in [64, 128, 256]:
            src_path = ICONS_DIR / str(src_size) / filename
            if src_path.exists():
                break
        else:
            print(f"Warning: Icon not found: {filename}")
            continue

        print(f"  Converting: {filename} (tiny)")

        width, height, stride, pixels = convert_png_to_lvgl(src_path, ICON_SIZE_INFO)
        source += f"static const uint8_t icon_{name}_data[] = {{\n"
        for i in range(0, len(pixels), 32):
            source += "    " + ", ".join(pixels[i:i+32]) + ",\n"
        source += "};\n\n"

        source += f'''const lv_image_dsc_t icon_{name} = {{
    .header = {{
        .magic = LV_IMAGE_HEADER_MAGIC,
        .cf = LV_COLOR_FORMAT_RGB565,
        .flags = 0,
        .w = {width},
        .h = {height},
        .stride = {stride},
        .reserved_2 = 0,
    }},
    .data_size = sizeof(icon_{name}_data),
    .data = icon_{name}_data,
}};

'''

    # Add lookup functions
    source += '''
const lv_image_dsc_t* get_weather_icon(int wmo_code, bool is_day, bool small_size)
{
    const lv_image_dsc_t* icon = NULL;
    
    if (wmo_code == 0) {
        icon = is_day ? (small_size ? &weather_icon_clear_day_small : &weather_icon_clear_day)
                      : (small_size ? &weather_icon_clear_night_small : &weather_icon_clear_night);
    }
    else if (wmo_code <= 3) {
        icon = is_day ? (small_size ? &weather_icon_partly_cloudy_day_small : &weather_icon_partly_cloudy_day)
                      : (small_size ? &weather_icon_partly_cloudy_night_small : &weather_icon_partly_cloudy_night);
    }
    else if (wmo_code <= 48) {
        icon = small_size ? &weather_icon_fog_small : &weather_icon_fog;
    }
    else if (wmo_code <= 57) {
        icon = small_size ? &weather_icon_drizzle_small : &weather_icon_drizzle;
    }
    else if (wmo_code <= 67) {
        icon = small_size ? &weather_icon_rain_small : &weather_icon_rain;
    }
    else if (wmo_code <= 77) {
        icon = small_size ? &weather_icon_snow_small : &weather_icon_snow;
    }
    else if (wmo_code <= 82) {
        icon = small_size ? &weather_icon_rain_small : &weather_icon_rain;
    }
    else if (wmo_code <= 86) {
        icon = small_size ? &weather_icon_snow_small : &weather_icon_snow;
    }
    else if (wmo_code >= 95) {
        icon = small_size ? &weather_icon_thunderstorm_small : &weather_icon_thunderstorm;
    }
    else {
        icon = small_size ? &weather_icon_cloudy_small : &weather_icon_cloudy;
    }

    return icon;
}

const lv_image_dsc_t* get_wind_icon(int wind_speed_mph)
{
    // Convert wind speed to Beaufort scale
    int beaufort;
    if (wind_speed_mph < 1) beaufort = 0;
    else if (wind_speed_mph <= 3) beaufort = 1;
    else if (wind_speed_mph <= 7) beaufort = 2;
    else if (wind_speed_mph <= 12) beaufort = 3;
    else if (wind_speed_mph <= 18) beaufort = 4;
    else if (wind_speed_mph <= 24) beaufort = 5;
    else if (wind_speed_mph <= 31) beaufort = 6;
    else if (wind_speed_mph <= 38) beaufort = 7;
    else if (wind_speed_mph <= 46) beaufort = 8;
    else if (wind_speed_mph <= 54) beaufort = 9;
    else if (wind_speed_mph <= 63) beaufort = 10;
    else if (wind_speed_mph <= 72) beaufort = 11;
    else beaufort = 12;

    switch (beaufort) {
        case 0: return &icon_wind_0;
        case 1: return &icon_wind_1;
        case 2: return &icon_wind_2;
        case 3: return &icon_wind_3;
        case 4: return &icon_wind_4;
        case 5: return &icon_wind_5;
        case 6: return &icon_wind_6;
        case 7: return &icon_wind_7;
        case 8: return &icon_wind_8;
        case 9: return &icon_wind_9;
        case 10: return &icon_wind_10;
        case 11: return &icon_wind_11;
        case 12: return &icon_wind_12;
        default: return &icon_wind_3;
    }
}
'''

    with open(OUTPUT_DIR / "weather_icons.cpp", 'w') as f:
        f.write(source)
    print(f"Generated: weather_icons.cpp")
    
    print("Done!")

if __name__ == "__main__":
    main()
