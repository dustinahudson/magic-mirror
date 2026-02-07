# Magic Mirror

A bare-metal magic mirror application for Raspberry Pi Zero W using [Circle OS](https://github.com/rsta2/circle).

## Features

- **DateTime Widget**: Current date and time with seconds display
- **Current Weather Widget**: Temperature, feels like, wind, and sunset time
- **Weather Forecast Widget**: 5-day forecast with icons
- **Upcoming Events Widget**: Aggregated events from multiple calendars
- **Calendar Widget**: Monthly grid view with events

## Requirements

### Hardware
- Raspberry Pi Zero W
- HDMI display (1920x1080)
- MicroSD card (8GB+)
- WiFi network

### Software
- ARM cross-compiler (`arm-none-eabi-gcc`)
- Make, wget, git

## Building

1. Initialize the build environment (clones and builds all dependencies):
```bash
./init.sh
```

2. Build the Magic Mirror:
```bash
make
```

## Deployment

1. Copy the example config files and edit them:
```bash
cp config/config.json.example config/config.json
cp wpa_supplicant.conf.example wpa_supplicant.conf
```

2. Edit `wpa_supplicant.conf` with your WiFi credentials:
```
country=US

network={
	ssid="YOUR_WIFI_SSID"
	psk="YOUR_WIFI_PASSWORD"
	proto=WPA2
	key_mgmt=WPA-PSK
}
```

3. Edit `config/config.json` with your settings:
   - Set your timezone and zipcode
   - Add calendar iCal URLs (Google Calendar: Settings > Integrate calendar > Secret address in iCal format)

4. Insert a MicroSD card and run the deploy script:
```bash
./deploy.sh /media/$USER/BOOT
```

The script will:
- Download Pi boot firmware and WiFi firmware if not already cached
- Check the SD card filesystem (reformats to FAT32 if needed)
- Copy boot firmware, kernel, config, and WiFi firmware
- Copy `wpa_supplicant.conf` and `config/config.json`

Use `./deploy.sh --setup /path/to/sd` to force a full re-setup of all boot files.

## USB Serial Logging

Connect to the Pi's UART for debug output:
```bash
screen /dev/ttyUSB0 115200
```

## Architecture

```
src/
├── core/           # Application core (kernel, main loop)
├── config/         # Configuration parsing
├── modules/
│   └── widgets/    # UI widgets (datetime, weather, calendar, events)
├── services/       # HTTP client, weather API, calendar/ICS parsing
└── ui/             # Display, grid layout
```

## License

[MIT](LICENSE)
