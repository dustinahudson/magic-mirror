#include "config/config.h"
#include <circle/util.h>
#include <circle/logger.h>
#include <fatfs/ff.h>
#include <string.h>
#include <stdlib.h>
#include <stdio.h>
#include <ctype.h>

namespace mm {

static const char FromConfig[] = "config";

// POSIX timezone string for common timezones
// Format: "STD offset DST,start,end" e.g., "CST6CDT,M3.2.0,M11.1.0"
struct TimezoneInfo {
    const char* name;
    const char* posixTz;
};

static const TimezoneInfo g_timezones[] = {
    // US timezones
    {"America/New_York",    "EST5EDT,M3.2.0,M11.1.0"},
    {"America/Chicago",     "CST6CDT,M3.2.0,M11.1.0"},
    {"America/Denver",      "MST7MDT,M3.2.0,M11.1.0"},
    {"America/Los_Angeles", "PST8PDT,M3.2.0,M11.1.0"},
    {"America/Anchorage",   "AKST9AKDT,M3.2.0,M11.1.0"},
    {"America/Phoenix",     "MST7"},  // Arizona - no DST
    {"Pacific/Honolulu",    "HST10"}, // Hawaii - no DST
    {"US/Eastern",          "EST5EDT,M3.2.0,M11.1.0"},
    {"US/Central",          "CST6CDT,M3.2.0,M11.1.0"},
    {"US/Mountain",         "MST7MDT,M3.2.0,M11.1.0"},
    {"US/Pacific",          "PST8PDT,M3.2.0,M11.1.0"},

    // Europe (last Sunday of March to last Sunday of October)
    {"Europe/London",       "GMT0BST,M3.5.0/1,M10.5.0"},
    {"Europe/Paris",        "CET-1CEST,M3.5.0,M10.5.0/3"},
    {"Europe/Berlin",       "CET-1CEST,M3.5.0,M10.5.0/3"},

    // UTC
    {"UTC",                 "UTC0"},
    {"GMT",                 "GMT0"},
    {"Etc/UTC",             "UTC0"},

    {nullptr, nullptr}
};

// Get the Nth weekday of a month (e.g., 2nd Sunday of March)
// week: 1-4 for 1st-4th, 5 for last
// weekday: 0=Sunday, 1=Monday, etc.
static unsigned GetNthWeekday(unsigned year, unsigned month, unsigned week, unsigned weekday)
{
    // Days in each month
    static const unsigned daysInMonth[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};
    bool isLeap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
    unsigned maxDay = daysInMonth[month - 1];
    if (month == 2 && isLeap) maxDay = 29;

    // Calculate day of week for the 1st of the month
    // Using Zeller's formula simplified for Gregorian calendar
    unsigned y = year;
    unsigned m = month;
    if (m < 3) { m += 12; y--; }
    unsigned q = 1;  // First day of month
    unsigned k = y % 100;
    unsigned j = y / 100;
    unsigned h = (q + (13 * (m + 1)) / 5 + k + k / 4 + j / 4 - 2 * j) % 7;
    // h: 0=Sat, 1=Sun, 2=Mon, etc. Convert to 0=Sun
    unsigned firstDayOfWeek = (h + 6) % 7;

    // Find the first occurrence of the target weekday
    int firstOccurrence = 1 + ((int)weekday - (int)firstDayOfWeek + 7) % 7;

    // Calculate the Nth occurrence
    unsigned day;
    if (week == 5) {
        // Last occurrence of the weekday in the month
        day = firstOccurrence + 21;  // 4th occurrence
        if (day + 7 <= maxDay) day += 7;  // 5th if it exists
    } else {
        day = firstOccurrence + (week - 1) * 7;
    }

    return day;
}

// Parse POSIX timezone rule: Mm.n.d (month.week.weekday)
// Returns day of month for the transition
static unsigned ParsePosixRule(const char* rule, unsigned year, unsigned* hour)
{
    *hour = 2;  // Default transition at 2:00 AM

    if (rule[0] != 'M') return 0;

    unsigned month = 0, week = 0, weekday = 0;
    const char* p = rule + 1;

    month = atoi(p);
    p = strchr(p, '.');
    if (!p) return 0;
    p++;

    week = atoi(p);
    p = strchr(p, '.');
    if (!p) return 0;
    p++;

    weekday = atoi(p);

    // Check for optional /hour
    p = strchr(p, '/');
    if (p) {
        *hour = atoi(p + 1);
    }

    return GetNthWeekday(year, month, week, weekday);
}

// Calculate timezone offset for a given UTC timestamp using POSIX tz string
static int CalculateOffset(const char* posixTz, unsigned utcTimestamp)
{
    // Parse standard offset (e.g., "CST6" -> -6 hours)
    const char* p = posixTz;
    while (*p && !isdigit(*p) && *p != '-' && *p != '+') p++;  // Skip std name

    int stdOffset = 0;
    bool negative = true;  // POSIX uses positive west, we use negative
    if (*p == '-') { negative = false; p++; }
    else if (*p == '+') { negative = true; p++; }

    stdOffset = atoi(p) * 3600;
    if (negative) stdOffset = -stdOffset;

    // Skip to DST part
    while (*p && (isdigit(*p) || *p == ':')) p++;  // Skip offset digits
    if (!*p || *p == ',') {
        // No DST
        return stdOffset;
    }

    // Skip DST name
    while (*p && !isdigit(*p) && *p != '-' && *p != '+' && *p != ',') p++;

    // Parse DST offset (default is std + 1 hour)
    int dstOffset = stdOffset + 3600;
    if (*p && *p != ',') {
        negative = true;
        if (*p == '-') { negative = false; p++; }
        else if (*p == '+') { negative = true; p++; }
        dstOffset = atoi(p) * 3600;
        if (negative) dstOffset = -dstOffset;
        while (*p && (isdigit(*p) || *p == ':')) p++;
    }

    // Parse transition rules
    if (*p != ',') return stdOffset;  // No DST rules
    p++;

    const char* startRule = p;
    p = strchr(p, ',');
    if (!p) return stdOffset;
    p++;
    const char* endRule = p;

    // Convert UTC timestamp to year/month/day
    unsigned days = utcTimestamp / 86400;
    unsigned year = 1970;
    while (true) {
        bool isLeap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
        unsigned daysInYear = isLeap ? 366 : 365;
        if (days < daysInYear) break;
        days -= daysInYear;
        year++;
    }

    // Calculate DST start and end timestamps for this year
    unsigned startHour, endHour;
    unsigned startDay = ParsePosixRule(startRule, year, &startHour);
    unsigned endDay = ParsePosixRule(endRule, year, &endHour);

    // Parse month from rules
    unsigned startMonth = atoi(startRule + 1);
    unsigned endMonth = atoi(endRule + 1);

    // Calculate day of year for start and end
    static const unsigned daysBeforeMonth[] = {0, 31, 59, 90, 120, 151, 181, 212, 243, 273, 304, 334};
    bool isLeap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);

    unsigned startDayOfYear = daysBeforeMonth[startMonth - 1] + startDay - 1;
    if (startMonth > 2 && isLeap) startDayOfYear++;

    unsigned endDayOfYear = daysBeforeMonth[endMonth - 1] + endDay - 1;
    if (endMonth > 2 && isLeap) endDayOfYear++;

    // Current day of year
    unsigned currentDayOfYear = days;
    unsigned currentSecondOfDay = utcTimestamp % 86400;

    // Determine if we're in DST
    // Note: DST transitions are in local standard time
    bool inDst = false;
    if (startMonth < endMonth) {
        // Northern hemisphere: DST from spring to fall
        if (currentDayOfYear > startDayOfYear && currentDayOfYear < endDayOfYear) {
            inDst = true;
        } else if (currentDayOfYear == startDayOfYear) {
            inDst = (currentSecondOfDay + stdOffset) >= (startHour * 3600);
        } else if (currentDayOfYear == endDayOfYear) {
            inDst = (currentSecondOfDay + dstOffset) < (endHour * 3600);
        }
    } else {
        // Southern hemisphere: DST from fall to spring (crosses year boundary)
        if (currentDayOfYear > startDayOfYear || currentDayOfYear < endDayOfYear) {
            inDst = true;
        } else if (currentDayOfYear == startDayOfYear) {
            inDst = (currentSecondOfDay + stdOffset) >= (startHour * 3600);
        } else if (currentDayOfYear == endDayOfYear) {
            inDst = (currentSecondOfDay + dstOffset) < (endHour * 3600);
        }
    }

    return inDst ? dstOffset : stdOffset;
}

// Look up POSIX timezone string for a timezone name
static const char* GetPosixTz(const char* tzName)
{
    for (int i = 0; g_timezones[i].name != nullptr; i++) {
        if (strcmp(tzName, g_timezones[i].name) == 0) {
            return g_timezones[i].posixTz;
        }
        // Also check for partial matches (e.g., "Chicago" matches "America/Chicago")
        if (strstr(g_timezones[i].name, tzName) != nullptr) {
            return g_timezones[i].posixTz;
        }
    }
    // Return as-is if it looks like a POSIX string already
    if (strchr(tzName, ',') != nullptr || isdigit(tzName[0]) ||
        (strlen(tzName) >= 4 && isdigit(tzName[3]))) {
        return tzName;
    }
    return "UTC0";  // Default
}

// Get timezone offset for a specific UTC timestamp (handles DST)
int GetTimezoneOffset(const char* tzName, unsigned utcTimestamp)
{
    const char* posixTz = GetPosixTz(tzName);
    return CalculateOffset(posixTz, utcTimestamp);
}

void Config::GetDefault(Config* pConfig)
{
    memset(pConfig, 0, sizeof(Config));

    strcpy(pConfig->timezone, "UTC");

    strcpy(pConfig->weather.zipcode, "");
    strcpy(pConfig->weather.units, "imperial");

    pConfig->grid.columns = 12;
    pConfig->grid.rows = 8;
    pConfig->grid.paddingX = 30;
    pConfig->grid.paddingY = 30;
    pConfig->grid.gapX = 15;
    pConfig->grid.gapY = 15;

    pConfig->nWidgets = 0;
    pConfig->nCalendars = 0;

    pConfig->update.enabled = false;
}

// Simple JSON helper functions
static const char* SkipWhitespace(const char* p)
{
    while (*p && (*p == ' ' || *p == '\t' || *p == '\n' || *p == '\r')) {
        p++;
    }
    return p;
}

static const char* FindKey(const char* json, const char* key)
{
    char searchKey[128];
    snprintf(searchKey, sizeof(searchKey), "\"%s\"", key);

    const char* pos = strstr(json, searchKey);
    if (!pos) return nullptr;

    pos += strlen(searchKey);
    pos = SkipWhitespace(pos);
    if (*pos != ':') return nullptr;
    pos++;
    pos = SkipWhitespace(pos);

    return pos;
}

static bool ParseString(const char* p, char* out, int maxLen, const char** endPtr)
{
    p = SkipWhitespace(p);
    if (*p != '"') return false;
    p++;

    int i = 0;
    while (*p && *p != '"' && i < maxLen - 1) {
        if (*p == '\\' && *(p + 1)) {
            p++;  // Skip escape char
        }
        out[i++] = *p++;
    }
    out[i] = '\0';

    if (*p == '"') p++;
    if (endPtr) *endPtr = p;
    return true;
}

static const char* FindArrayStart(const char* p)
{
    p = SkipWhitespace(p);
    if (*p != '[') return nullptr;
    return p + 1;
}

static const char* FindObjectStart(const char* p)
{
    p = SkipWhitespace(p);
    if (*p != '{') return nullptr;
    return p + 1;
}

static const char* FindObjectEnd(const char* p)
{
    int depth = 1;
    while (*p && depth > 0) {
        if (*p == '{') depth++;
        else if (*p == '}') depth--;
        else if (*p == '"') {
            // Skip string contents
            p++;
            while (*p && *p != '"') {
                if (*p == '\\' && *(p + 1)) p++;
                p++;
            }
        }
        if (depth > 0) p++;
    }
    return p;
}

boolean Config::LoadFromFile(const char* path, Config* pConfig)
{
    // Start with defaults
    GetDefault(pConfig);

    // Open and read config file
    FIL file;
    if (f_open(&file, path, FA_READ) != FR_OK) {
        CLogger::Get()->Write(FromConfig, LogWarning, "Cannot open config: %s", path);
        return FALSE;
    }

    // Get file size
    UINT fileSize = f_size(&file);
    if (fileSize > 8192) {
        fileSize = 8192;  // Limit to 8KB
    }

    // Read file
    static char jsonBuffer[8192];
    UINT bytesRead;
    if (f_read(&file, jsonBuffer, fileSize, &bytesRead) != FR_OK) {
        f_close(&file);
        CLogger::Get()->Write(FromConfig, LogWarning, "Cannot read config file");
        return FALSE;
    }
    jsonBuffer[bytesRead] = '\0';
    f_close(&file);

    CLogger::Get()->Write(FromConfig, LogNotice, "Read %u bytes from config", bytesRead);

    // Parse timezone
    const char* timezoneVal = FindKey(jsonBuffer, "timezone");
    if (timezoneVal) {
        ParseString(timezoneVal, pConfig->timezone, sizeof(pConfig->timezone), nullptr);
        CLogger::Get()->Write(FromConfig, LogNotice, "Timezone: %s", pConfig->timezone);
    }

    // Parse weather section
    const char* weatherSection = FindKey(jsonBuffer, "weather");
    if (weatherSection) {
        const char* zipcode = FindKey(weatherSection, "zipcode");
        if (zipcode) {
            ParseString(zipcode, pConfig->weather.zipcode, sizeof(pConfig->weather.zipcode), nullptr);
        }
        const char* units = FindKey(weatherSection, "units");
        if (units) {
            ParseString(units, pConfig->weather.units, sizeof(pConfig->weather.units), nullptr);
        }
    }

    // Parse calendars array
    const char* calendarsKey = FindKey(jsonBuffer, "calendars");
    if (calendarsKey) {
        const char* arrayStart = FindArrayStart(calendarsKey);
        if (arrayStart) {
            const char* p = arrayStart;
            pConfig->nCalendars = 0;

            while (*p && pConfig->nCalendars < MAX_CALENDARS) {
                p = SkipWhitespace(p);
                if (*p == ']') break;
                if (*p == ',') { p++; continue; }

                // Find object start
                const char* objStart = FindObjectStart(p);
                if (!objStart) break;

                CalendarConfig& cal = pConfig->calendars[pConfig->nCalendars];
                memset(&cal, 0, sizeof(cal));

                // Find object end to limit search scope
                const char* objEnd = FindObjectEnd(objStart);

                // Parse url
                const char* url = FindKey(objStart, "url");
                if (url && url < objEnd) {
                    ParseString(url, cal.url, sizeof(cal.url), nullptr);
                }

                // Parse name
                const char* name = FindKey(objStart, "name");
                if (name && name < objEnd) {
                    ParseString(name, cal.name, sizeof(cal.name), nullptr);
                }

                // Parse color
                const char* color = FindKey(objStart, "color");
                if (color && color < objEnd) {
                    ParseString(color, cal.color, sizeof(cal.color), nullptr);
                }

                // Only add if we got a URL
                if (cal.url[0] != '\0') {
                    pConfig->nCalendars++;
                    CLogger::Get()->Write(FromConfig, LogNotice, "Calendar: %s (%s)",
                                         cal.name, cal.color);
                }

                p = objEnd;
                if (*p == '}') p++;
            }
        }
    }

    CLogger::Get()->Write(FromConfig, LogNotice, "Loaded %d calendars", pConfig->nCalendars);

    // Parse update section
    const char* updateSection = FindKey(jsonBuffer, "update");
    if (updateSection) {
        const char* enabled = FindKey(updateSection, "enabled");
        if (enabled) {
            // Simple boolean parse: check for "true"
            const char* ep = SkipWhitespace(enabled);
            pConfig->update.enabled = (strncmp(ep, "true", 4) == 0);
        }
        CLogger::Get()->Write(FromConfig, LogNotice, "Update: enabled=%s",
                             pConfig->update.enabled ? "true" : "false");
    }

    return TRUE;
}

} // namespace mm
