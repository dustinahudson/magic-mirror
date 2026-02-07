#ifndef CONFIG_H
#define CONFIG_H

#include <circle/string.h>

namespace mm {

// Fixed-size string buffer
static const int MAX_STRING_LEN = 256;
static const int MAX_URL_LEN = 512;
static const int MAX_CALENDARS = 10;
static const int MAX_WIDGETS = 10;

struct CalendarConfig {
    char url[MAX_URL_LEN];
    char name[64];
    char color[16];
};

struct WidgetPosition {
    int gridX;
    int gridY;
    int gridWidth;
    int gridHeight;
};

struct WidgetConfig {
    char type[32];
    char id[32];
    WidgetPosition position;
};

struct GridConfig {
    int columns;
    int rows;
    int paddingX;
    int paddingY;
    int gapX;
    int gapY;
};

struct WeatherConfig {
    char zipcode[16];
    char units[16];
};

// Get timezone offset for a specific UTC timestamp (handles DST)
int GetTimezoneOffset(const char* tzName, unsigned utcTimestamp);

struct Config {
    char timezone[64];
    WeatherConfig weather;
    GridConfig grid;
    CalendarConfig calendars[MAX_CALENDARS];
    int nCalendars;
    WidgetConfig widgets[MAX_WIDGETS];
    int nWidgets;

    static boolean LoadFromFile(const char* path, Config* pConfig);
    static void GetDefault(Config* pConfig);
};

} // namespace mm

#endif // CONFIG_H
