#include "services/ics_stream_parser.h"
#include <circle/logger.h>
#include <string.h>
#include <stdio.h>
#include <ctype.h>

namespace mm {

static const char FromICS[] = "ics";

// Structure to hold parsed RRULE with full support
struct RRule {
    enum Freq { NONE, DAILY, WEEKLY, MONTHLY, YEARLY } freq;
    unsigned interval;
    unsigned until;           // 0 if no UNTIL
    int count;                // -1 if no COUNT

    // BYDAY support - multiple days with optional week positions
    // For WEEKLY: byDayMask is a bitmask of days (bit 0=SU, bit 1=MO, etc.)
    // For MONTHLY: byDayEntries stores positional entries like -1FR, 2MO
    unsigned char byDayMask;  // Bitmask: bit 0=SU, 1=MO, 2=TU, 3=WE, 4=TH, 5=FR, 6=SA
    struct ByDayEntry {
        signed char week;     // 0=every, 1-5=Nth, -1=last, -2=2nd last, etc.
        signed char day;      // 0-6 for SU-SA, -1 if unused
    };
    static const int MAX_BYDAY = 8;
    ByDayEntry byDayEntries[MAX_BYDAY];
    int byDayCount;

    // BYMONTH support - bitmask of months (bit 0=Jan, bit 11=Dec)
    unsigned short byMonthMask;

    // BYMONTHDAY support - multiple days
    static const int MAX_BYMONTHDAY = 8;
    signed char byMonthDays[MAX_BYMONTHDAY];  // Can be negative for "from end"
    int byMonthDayCount;

    // BYSETPOS - position within the set
    int bySetPos;             // 0 if not set, positive for Nth, negative for from end

    // WKST - week start day (0=SU default, 1=MO, etc.)
    int wkst;
};

// Helper to parse a day abbreviation, returns 0-6 or -1 if invalid
static int ParseDayAbbrev(const char* pos)
{
    if (strncmp(pos, "SU", 2) == 0) return 0;
    if (strncmp(pos, "MO", 2) == 0) return 1;
    if (strncmp(pos, "TU", 2) == 0) return 2;
    if (strncmp(pos, "WE", 2) == 0) return 3;
    if (strncmp(pos, "TH", 2) == 0) return 4;
    if (strncmp(pos, "FR", 2) == 0) return 5;
    if (strncmp(pos, "SA", 2) == 0) return 6;
    return -1;
}

// Helper to parse an integer from string, advancing pos
static int ParseInt(const char*& pos)
{
    bool negative = false;
    if (*pos == '-') {
        negative = true;
        pos++;
    } else if (*pos == '+') {
        pos++;
    }
    int val = 0;
    while (*pos >= '0' && *pos <= '9') {
        val = val * 10 + (*pos - '0');
        pos++;
    }
    return negative ? -val : val;
}

// Helper to convert date to timestamp
static unsigned DateToTimestampHelper(int year, int month, int day)
{
    static const int daysInMonth[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};
    unsigned days = 0;
    for (int y = 1970; y < year; y++) {
        bool leap = (y % 4 == 0 && y % 100 != 0) || (y % 400 == 0);
        days += leap ? 366 : 365;
    }
    bool leap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
    for (int m = 0; m < month - 1; m++) {
        days += daysInMonth[m];
        if (m == 1 && leap) days++;
    }
    days += day - 1;
    return days * 86400;
}

static bool ParseRRule(const char* value, RRule* rule)
{
    memset(rule, 0, sizeof(*rule));
    rule->freq = RRule::NONE;
    rule->interval = 1;
    rule->count = -1;
    rule->bySetPos = 0;
    rule->wkst = 1;  // Monday default (common in business calendars)

    // Initialize byDayEntries
    for (int i = 0; i < RRule::MAX_BYDAY; i++) {
        rule->byDayEntries[i].day = -1;
        rule->byDayEntries[i].week = 0;
    }

    const char* pos = value;

    while (*pos) {
        if (strncmp(pos, "FREQ=", 5) == 0) {
            pos += 5;
            if (strncmp(pos, "DAILY", 5) == 0) rule->freq = RRule::DAILY;
            else if (strncmp(pos, "WEEKLY", 6) == 0) rule->freq = RRule::WEEKLY;
            else if (strncmp(pos, "MONTHLY", 7) == 0) rule->freq = RRule::MONTHLY;
            else if (strncmp(pos, "YEARLY", 6) == 0) rule->freq = RRule::YEARLY;
        }
        else if (strncmp(pos, "INTERVAL=", 9) == 0) {
            pos += 9;
            rule->interval = ParseInt(pos);
            if (rule->interval == 0) rule->interval = 1;
            continue;
        }
        else if (strncmp(pos, "COUNT=", 6) == 0) {
            pos += 6;
            rule->count = ParseInt(pos);
            continue;
        }
        else if (strncmp(pos, "UNTIL=", 6) == 0) {
            pos += 6;
            int year = 0, month = 0, day = 0;
            if (sscanf(pos, "%4d%2d%2d", &year, &month, &day) == 3) {
                rule->until = DateToTimestampHelper(year, month, day) + 86400;
            }
        }
        else if (strncmp(pos, "BYDAY=", 6) == 0) {
            pos += 6;
            // Parse comma-separated list: "MO,WE,FR" or "-1FR" or "2MO,4MO"
            while (*pos && *pos != ';') {
                // Parse optional week number
                int week = 0;
                if (*pos == '-' || (*pos >= '0' && *pos <= '9')) {
                    week = ParseInt(pos);
                }

                // Parse day abbreviation
                int day = ParseDayAbbrev(pos);
                if (day >= 0) {
                    pos += 2;

                    // Add to bitmask for simple cases (weekly)
                    if (week == 0) {
                        rule->byDayMask |= (1 << day);
                    }

                    // Add to entries array for positional cases
                    if (rule->byDayCount < RRule::MAX_BYDAY) {
                        rule->byDayEntries[rule->byDayCount].week = week;
                        rule->byDayEntries[rule->byDayCount].day = day;
                        rule->byDayCount++;
                    }
                }

                // Skip comma
                if (*pos == ',') pos++;
            }
            continue;
        }
        else if (strncmp(pos, "BYMONTH=", 8) == 0) {
            pos += 8;
            // Parse comma-separated months: "1,6,12"
            while (*pos && *pos != ';') {
                int month = ParseInt(pos);
                if (month >= 1 && month <= 12) {
                    rule->byMonthMask |= (1 << (month - 1));
                }
                if (*pos == ',') pos++;
                else if (*pos != ';' && *pos != '\0') break;
            }
            continue;
        }
        else if (strncmp(pos, "BYMONTHDAY=", 11) == 0) {
            pos += 11;
            // Parse comma-separated days: "1,15,-1"
            while (*pos && *pos != ';') {
                int day = ParseInt(pos);
                if (day != 0 && rule->byMonthDayCount < RRule::MAX_BYMONTHDAY) {
                    rule->byMonthDays[rule->byMonthDayCount++] = day;
                }
                if (*pos == ',') pos++;
                else if (*pos != ';' && *pos != '\0') break;
            }
            continue;
        }
        else if (strncmp(pos, "BYSETPOS=", 9) == 0) {
            pos += 9;
            rule->bySetPos = ParseInt(pos);
            continue;
        }
        else if (strncmp(pos, "WKST=", 5) == 0) {
            pos += 5;
            int day = ParseDayAbbrev(pos);
            if (day >= 0) {
                rule->wkst = day;
                pos += 2;
            }
            continue;
        }

        // Skip to next parameter
        while (*pos && *pos != ';') pos++;
        if (*pos == ';') pos++;
    }

    return rule->freq != RRule::NONE;
}

// Get Nth weekday of a month (for RRULE expansion)
static unsigned GetNthWeekdayOfMonthForRRule(unsigned year, unsigned month, int week, int weekday)
{
    static const unsigned daysInMonth[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};
    bool isLeap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
    unsigned maxDay = daysInMonth[month - 1];
    if (month == 2 && isLeap) maxDay = 29;

    // Zeller's formula for day of week of 1st of month
    unsigned y = year, m = month;
    if (m < 3) { m += 12; y--; }
    unsigned k = y % 100, j = y / 100;
    unsigned h = (1 + (13 * (m + 1)) / 5 + k + k / 4 + j / 4 - 2 * j) % 7;
    unsigned firstDayOfWeek = (h + 6) % 7;  // Convert to 0=Sunday

    int firstOccurrence = 1 + ((weekday - (int)firstDayOfWeek + 7) % 7);

    unsigned day;
    if (week == -1) {
        // Last occurrence
        day = firstOccurrence + 21;
        if (day + 7 <= maxDay) day += 7;
    } else if (week > 0) {
        day = firstOccurrence + (week - 1) * 7;
    } else {
        day = firstOccurrence;  // First occurrence if week is 0
    }

    if (day > maxDay) return 0;  // Invalid
    return day;
}

ICSStreamParser::ICSStreamParser()
    : m_bufferLen(0),
      m_windowStart(0),
      m_windowEnd(0),
      m_timezoneOffset(0),
      m_callback(nullptr),
      m_callbackUserData(nullptr),
      m_eventCount(0),
      m_skippedCount(0),
      m_recurrenceIdCount(0)
{
    m_calendarColor[0] = '\0';
    m_buffer[0] = '\0';
}

ICSStreamParser::~ICSStreamParser()
{
}

void ICSStreamParser::SetCalendarColor(const char* color)
{
    strncpy(m_calendarColor, color, sizeof(m_calendarColor) - 1);
    m_calendarColor[sizeof(m_calendarColor) - 1] = '\0';
}

void ICSStreamParser::SetTimeWindow(unsigned startTime, unsigned endTime)
{
    m_windowStart = startTime;
    m_windowEnd = endTime;
}

void ICSStreamParser::SetEventCallback(ICSEventCallback callback, void* userData)
{
    m_callback = callback;
    m_callbackUserData = userData;
}

void ICSStreamParser::FeedData(const char* data, unsigned length)
{
    // Append to buffer, processing complete events as we find them
    unsigned remaining = length;
    const char* ptr = data;

    while (remaining > 0) {
        // How much can we fit in the buffer?
        unsigned space = BUFFER_SIZE - m_bufferLen - 1;
        unsigned toCopy = (remaining < space) ? remaining : space;

        if (toCopy > 0) {
            memcpy(m_buffer + m_bufferLen, ptr, toCopy);
            m_bufferLen += toCopy;
            m_buffer[m_bufferLen] = '\0';
            ptr += toCopy;
            remaining -= toCopy;
        }

        // Process any complete events in the buffer
        ProcessBuffer();

        // If buffer is full and we couldn't process anything, we have a problem
        // (event too large for buffer) - skip ahead to next event
        if (m_bufferLen >= BUFFER_SIZE - 1 && remaining > 0) {
            CLogger::Get()->Write(FromICS, LogWarning, "Event too large, skipping");
            // Try to find END:VEVENT and skip past it
            char* endEvent = strstr(m_buffer, "END:VEVENT");
            if (endEvent) {
                unsigned skipLen = (endEvent - m_buffer) + 10;
                memmove(m_buffer, m_buffer + skipLen, m_bufferLen - skipLen + 1);
                m_bufferLen -= skipLen;
            } else {
                // Can't find end, clear buffer and hope for the best
                m_bufferLen = 0;
                m_buffer[0] = '\0';
            }
        }
    }
}

void ICSStreamParser::Finish()
{
    // Process any remaining data
    ProcessBuffer();

    CLogger::Get()->Write(FromICS, LogNotice, "Parsed %d events, skipped %d past/out-of-range",
                          m_eventCount, m_skippedCount);
}

// Convert a timestamp to year/month/day
static void TimestampToDate(unsigned timestamp, unsigned* year, unsigned* month, unsigned* day)
{
    static const int daysInMonth[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};
    unsigned days = timestamp / 86400;

    // Find year
    unsigned y = 1970;
    while (true) {
        bool leap = (y % 4 == 0 && y % 100 != 0) || (y % 400 == 0);
        unsigned daysInYear = leap ? 366 : 365;
        if (days < daysInYear) break;
        days -= daysInYear;
        y++;
    }
    *year = y;

    // Find month
    bool leap = (y % 4 == 0 && y % 100 != 0) || (y % 400 == 0);
    unsigned m = 1;
    while (m <= 12) {
        unsigned dim = daysInMonth[m - 1];
        if (m == 2 && leap) dim = 29;
        if (days < dim) break;
        days -= dim;
        m++;
    }
    *month = m;
    *day = days + 1;
}

// Convert year/month/day/time to timestamp
static unsigned DateToTimestamp(unsigned year, unsigned month, unsigned day,
                                 unsigned hour, unsigned minute, unsigned second)
{
    static const int daysInMonth[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};
    unsigned days = 0;

    for (unsigned y = 1970; y < year; y++) {
        bool leap = (y % 4 == 0 && y % 100 != 0) || (y % 400 == 0);
        days += leap ? 366 : 365;
    }

    bool leap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
    for (unsigned m = 0; m < month - 1; m++) {
        days += daysInMonth[m];
        if (m == 1 && leap) days++;
    }
    days += day - 1;

    return days * 86400 + hour * 3600 + minute * 60 + second;
}

void ICSStreamParser::ProcessBuffer()
{
    while (true) {
        // Find BEGIN:VEVENT
        char* eventStart = strstr(m_buffer, "BEGIN:VEVENT");
        if (!eventStart) {
            // No event start found - keep only the last part of buffer
            // in case BEGIN:VEVENT spans chunks
            if (m_bufferLen > 20) {
                // Keep last 20 chars in case "BEGIN:VEVENT" is split
                memmove(m_buffer, m_buffer + m_bufferLen - 20, 20);
                m_bufferLen = 20;
                m_buffer[m_bufferLen] = '\0';
            }
            return;
        }

        // Find END:VEVENT
        char* eventEnd = strstr(eventStart, "END:VEVENT");
        if (!eventEnd) {
            // Incomplete event - shift buffer to start at BEGIN:VEVENT
            if (eventStart > m_buffer) {
                unsigned shift = eventStart - m_buffer;
                memmove(m_buffer, eventStart, m_bufferLen - shift + 1);
                m_bufferLen -= shift;
            }
            return;
        }

        // We have a complete event - parse it
        CalendarEvent event;
        memset(&event, 0, sizeof(event));

        if (ParseEvent(eventStart, eventEnd, &event)) {
            // Check for RECURRENCE-ID (modified instance of recurring event)
            char recurrenceId[64] = {0};
            if (FindProperty(eventStart, eventEnd, "RECURRENCE-ID", recurrenceId, sizeof(recurrenceId))) {
                // This is an override event - track the date to skip when expanding RRULE
                const char* datePos = recurrenceId;
                const char* colonPos = strchr(recurrenceId, ':');
                if (colonPos) datePos = colonPos + 1;

                int year = 0, month = 0, day = 0;
                if (sscanf(datePos, "%4d%2d%2d", &year, &month, &day) == 3) {
                    if (m_recurrenceIdCount < MAX_RECURRENCE_IDS) {
                        m_recurrenceIds[m_recurrenceIdCount++] = DateToTimestampHelper(year, month, day);
                    }
                }

                // Emit the override event normally if in window
                if (IsEventInWindow(event.startTime, event.allDay)) {
                    m_eventCount++;
                    if (m_callback) {
                        m_callback(event, m_callbackUserData);
                    }
                }
            }
            // Check for RRULE (recurring event)
            else {
                char rruleStr[256] = {0};
                RRule rrule;
                bool hasRRule = FindProperty(eventStart, eventEnd, "RRULE", rruleStr, sizeof(rruleStr));

                if (hasRRule && ParseRRule(rruleStr, &rrule)) {
                    // Parse EXDATE (exception dates)
                    static const int MAX_EXDATES = 64;
                    unsigned exdates[MAX_EXDATES];
                    int exdateCount = 0;

                    // Include tracked RECURRENCE-ID dates as exceptions
                    for (int i = 0; i < m_recurrenceIdCount && exdateCount < MAX_EXDATES; i++) {
                        exdates[exdateCount++] = m_recurrenceIds[i];
                    }

                    // Find all EXDATE lines
                    const char* pos = eventStart;
                    while (pos < eventEnd && exdateCount < MAX_EXDATES) {
                        const char* exdateStart = strstr(pos, "EXDATE");
                        if (!exdateStart || exdateStart >= eventEnd) break;

                        // Find the colon
                        const char* colon = strchr(exdateStart, ':');
                        if (!colon || colon >= eventEnd) {
                            pos = exdateStart + 6;
                            continue;
                        }

                        // Find end of line
                        const char* eol = colon;
                        while (eol < eventEnd && *eol != '\r' && *eol != '\n') eol++;

                        // Parse comma-separated dates
                        const char* datePos = colon + 1;
                        while (datePos < eol && exdateCount < MAX_EXDATES) {
                            int year = 0, month = 0, day = 0;
                            if (sscanf(datePos, "%4d%2d%2d", &year, &month, &day) == 3) {
                                exdates[exdateCount++] = DateToTimestampHelper(year, month, day);
                            }
                            // Skip to next date (after comma or T and time)
                            while (datePos < eol && *datePos != ',' && *datePos != '\r' && *datePos != '\n') datePos++;
                            if (*datePos == ',') datePos++;
                        }

                        pos = eol;
                    }

                    // Expand recurring event with exception dates
                    ExpandRecurringEvent(event, rrule, exdates, exdateCount);
                } else {
                    // Regular event - check if in window
                    if (IsEventInWindow(event.startTime, event.allDay)) {
                        m_eventCount++;
                        if (m_callback) {
                            m_callback(event, m_callbackUserData);
                        }
                    } else {
                        m_skippedCount++;
                    }
                }
            }
        }

        // Remove processed event from buffer
        unsigned eventLen = (eventEnd + 10) - m_buffer;  // +10 for "END:VEVENT"
        if (eventLen < m_bufferLen) {
            memmove(m_buffer, m_buffer + eventLen, m_bufferLen - eventLen + 1);
            m_bufferLen -= eventLen;
        } else {
            m_bufferLen = 0;
            m_buffer[0] = '\0';
        }
    }
}

bool ICSStreamParser::IsEventInWindow(unsigned eventStart, bool isAllDay)
{
    if (isAllDay) {
        // All-day events are stored as midnight UTC on the date
        // Compare by day, not timestamp, to avoid timezone issues
        // An all-day event for "tomorrow" should be included even if
        // the current UTC time is past midnight of that day
        unsigned eventDay = eventStart / 86400;
        unsigned windowStartDay = m_windowStart / 86400;
        unsigned windowEndDay = m_windowEnd / 86400;
        return eventDay >= windowStartDay && eventDay <= windowEndDay;
    }
    return eventStart >= m_windowStart && eventStart <= m_windowEnd;
}

// Helper: get day of week for a date (0=Sunday)
static int GetDayOfWeek(unsigned year, unsigned month, unsigned day)
{
    unsigned y = year, m = month;
    if (m < 3) { m += 12; y--; }
    unsigned k = y % 100, j = y / 100;
    unsigned h = (day + (13 * (m + 1)) / 5 + k + k / 4 + j / 4 - 2 * j) % 7;
    return (h + 6) % 7;  // Convert to 0=Sunday
}

// Helper: get days in month
static unsigned GetDaysInMonthHelper(unsigned year, unsigned month)
{
    static const unsigned dim[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};
    bool leap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
    if (month == 2 && leap) return 29;
    return dim[month - 1];
}

// Helper: normalize a date after adding days
static void NormalizeDate(unsigned& year, unsigned& month, unsigned& day)
{
    while (day > GetDaysInMonthHelper(year, month)) {
        day -= GetDaysInMonthHelper(year, month);
        month++;
        if (month > 12) {
            month = 1;
            year++;
        }
    }
    while (day < 1) {
        month--;
        if (month < 1) {
            month = 12;
            year--;
        }
        day += GetDaysInMonthHelper(year, month);
    }
}

// Helper: advance month by interval
static void AdvanceMonth(unsigned& year, unsigned& month, unsigned interval)
{
    month += interval;
    while (month > 12) {
        month -= 12;
        year++;
    }
}

void ICSStreamParser::ExpandRecurringEvent(const CalendarEvent& baseEvent, const RRule& rule,
                                            const unsigned* exdates, int exdateCount)
{
    // Get base event's date/time components
    unsigned baseYear, baseMonth, baseDay;
    TimestampToDate(baseEvent.startTime, &baseYear, &baseMonth, &baseDay);

    // Get time-of-day and duration from base event
    unsigned baseTimeOfDay = baseEvent.startTime % 86400;
    unsigned duration = baseEvent.endTime - baseEvent.startTime;

    // Determine end date
    unsigned endTimestamp = m_windowEnd;
    if (rule.until > 0 && rule.until < endTimestamp) {
        endTimestamp = rule.until;
    }

    // Instance limits
    int maxInstances = 500;  // Safety limit
    if (rule.count > 0 && rule.count < maxInstances) {
        maxInstances = rule.count;
    }

    int totalInstanceCount = 0;  // Counts all instances (for COUNT)
    unsigned currentYear = baseYear;
    unsigned currentMonth = baseMonth;
    unsigned currentDay = baseDay;

    // For WEEKLY with multiple days
    (void)GetDayOfWeek(baseYear, baseMonth, baseDay);  // Available if needed for WKST calculations

    while (totalInstanceCount < maxInstances) {
        // Generate candidate dates for this period
        static const int MAX_CANDIDATES = 32;
        unsigned candidates[MAX_CANDIDATES];
        int candidateCount = 0;

        if (rule.freq == RRule::DAILY) {
            // Daily: one candidate per iteration
            candidates[candidateCount++] = DateToTimestamp(currentYear, currentMonth, currentDay, 0, 0, 0);
        }
        else if (rule.freq == RRule::WEEKLY) {
            // Weekly with BYDAY: generate all matching days in this week
            if (rule.byDayMask != 0) {
                // Find start of week containing currentDay
                int dow = GetDayOfWeek(currentYear, currentMonth, currentDay);
                int daysToWeekStart = (dow - rule.wkst + 7) % 7;
                unsigned wy = currentYear, wm = currentMonth;
                int wd = (int)currentDay - daysToWeekStart;
                if (wd < 1) {
                    wm--;
                    if (wm < 1) { wm = 12; wy--; }
                    wd += GetDaysInMonthHelper(wy, wm);
                }

                // Generate candidates for each day in byDayMask
                for (int d = 0; d < 7 && candidateCount < MAX_CANDIDATES; d++) {
                    int actualDay = (rule.wkst + d) % 7;
                    if (rule.byDayMask & (1 << actualDay)) {
                        unsigned cy = wy, cm = wm, cd = wd + d;
                        NormalizeDate(cy, cm, cd);
                        unsigned ts = DateToTimestamp(cy, cm, cd, 0, 0, 0);
                        // Skip if before base event
                        if (ts >= baseEvent.startTime - baseTimeOfDay) {
                            candidates[candidateCount++] = ts;
                        }
                    }
                }
            } else {
                // No BYDAY, use base day of week
                candidates[candidateCount++] = DateToTimestamp(currentYear, currentMonth, currentDay, 0, 0, 0);
            }
        }
        else if (rule.freq == RRule::MONTHLY) {
            // Check BYMONTH filter
            if (rule.byMonthMask != 0 && !(rule.byMonthMask & (1 << (currentMonth - 1)))) {
                AdvanceMonth(currentYear, currentMonth, rule.interval);
                continue;
            }

            if (rule.byDayCount > 0) {
                // BYDAY with positional (e.g., -1FR, 2MO)
                for (int i = 0; i < rule.byDayCount && candidateCount < MAX_CANDIDATES; i++) {
                    int week = rule.byDayEntries[i].week;
                    int day = rule.byDayEntries[i].day;
                    if (day < 0) continue;

                    unsigned d;
                    if (week == 0) {
                        // Every occurrence of this day in the month
                        d = GetNthWeekdayOfMonthForRRule(currentYear, currentMonth, 1, day);
                        while (d > 0 && d <= GetDaysInMonthHelper(currentYear, currentMonth)) {
                            if (candidateCount < MAX_CANDIDATES) {
                                candidates[candidateCount++] = DateToTimestamp(currentYear, currentMonth, d, 0, 0, 0);
                            }
                            d += 7;
                        }
                    } else {
                        d = GetNthWeekdayOfMonthForRRule(currentYear, currentMonth, week, day);
                        if (d > 0) {
                            candidates[candidateCount++] = DateToTimestamp(currentYear, currentMonth, d, 0, 0, 0);
                        }
                    }
                }
            }
            else if (rule.byMonthDayCount > 0) {
                // BYMONTHDAY (e.g., 1,15,-1)
                unsigned maxDay = GetDaysInMonthHelper(currentYear, currentMonth);
                for (int i = 0; i < rule.byMonthDayCount && candidateCount < MAX_CANDIDATES; i++) {
                    int d = rule.byMonthDays[i];
                    unsigned actualDay;
                    if (d > 0) {
                        actualDay = (unsigned)d;
                    } else {
                        // Negative: count from end
                        actualDay = maxDay + d + 1;
                    }
                    if (actualDay >= 1 && actualDay <= maxDay) {
                        candidates[candidateCount++] = DateToTimestamp(currentYear, currentMonth, actualDay, 0, 0, 0);
                    }
                }
            }
            else {
                // Default: same day of month as base
                unsigned maxDay = GetDaysInMonthHelper(currentYear, currentMonth);
                unsigned d = baseDay > maxDay ? maxDay : baseDay;
                candidates[candidateCount++] = DateToTimestamp(currentYear, currentMonth, d, 0, 0, 0);
            }
        }
        else if (rule.freq == RRule::YEARLY) {
            // Check BYMONTH filter
            if (rule.byMonthMask != 0) {
                for (int m = 1; m <= 12 && candidateCount < MAX_CANDIDATES; m++) {
                    if (rule.byMonthMask & (1 << (m - 1))) {
                        unsigned maxDay = GetDaysInMonthHelper(currentYear, m);
                        unsigned d = baseDay > maxDay ? maxDay : baseDay;
                        candidates[candidateCount++] = DateToTimestamp(currentYear, m, d, 0, 0, 0);
                    }
                }
            } else {
                candidates[candidateCount++] = DateToTimestamp(currentYear, baseMonth, baseDay, 0, 0, 0);
            }
        }

        // Sort candidates (simple insertion sort for small arrays)
        for (int i = 1; i < candidateCount; i++) {
            unsigned key = candidates[i];
            int j = i - 1;
            while (j >= 0 && candidates[j] > key) {
                candidates[j + 1] = candidates[j];
                j--;
            }
            candidates[j + 1] = key;
        }

        // Apply BYSETPOS if set
        if (rule.bySetPos != 0 && candidateCount > 0) {
            int pos = rule.bySetPos;
            int idx;
            if (pos > 0) {
                idx = pos - 1;
            } else {
                idx = candidateCount + pos;
            }
            if (idx >= 0 && idx < candidateCount) {
                candidates[0] = candidates[idx];
                candidateCount = 1;
            } else {
                candidateCount = 0;
            }
        }

        // Emit candidates that fall within window
        for (int i = 0; i < candidateCount && totalInstanceCount < maxInstances; i++) {
            unsigned instanceDate = candidates[i];
            unsigned instanceStart = instanceDate + baseTimeOfDay;

            // Skip if before base event start
            if (instanceStart < baseEvent.startTime) continue;

            // Check if past end
            if (instanceStart > endTimestamp) {
                return;  // Done
            }

            // Check EXDATE - skip if this date is an exception
            bool isExcluded = false;
            for (int e = 0; e < exdateCount; e++) {
                // Compare just the date portion (exdates are stored as midnight)
                if (instanceDate == exdates[e]) {
                    isExcluded = true;
                    break;
                }
            }
            if (isExcluded) {
                totalInstanceCount++;  // Still counts toward COUNT limit
                continue;
            }

            // Emit if in window
            // For all-day events, compare by day to avoid timezone issues
            bool inWindow = false;
            if (baseEvent.allDay) {
                unsigned instanceDay = instanceStart / 86400;
                unsigned windowStartDay = m_windowStart / 86400;
                inWindow = instanceDay >= windowStartDay;
            } else {
                inWindow = instanceStart >= m_windowStart;
            }
            if (inWindow) {
                CalendarEvent instance = baseEvent;
                instance.startTime = instanceStart;
                instance.endTime = instanceStart + duration;

                m_eventCount++;
                if (m_callback) {
                    m_callback(instance, m_callbackUserData);
                }
            }

            totalInstanceCount++;
        }

        // Advance to next period
        switch (rule.freq) {
            case RRule::DAILY:
                currentDay += rule.interval;
                NormalizeDate(currentYear, currentMonth, currentDay);
                break;

            case RRule::WEEKLY:
                currentDay += 7 * rule.interval;
                NormalizeDate(currentYear, currentMonth, currentDay);
                break;

            case RRule::MONTHLY:
                AdvanceMonth(currentYear, currentMonth, rule.interval);
                break;

            case RRule::YEARLY:
                currentYear += rule.interval;
                break;

            default:
                return;
        }

        // Safety: check if we've gone past the end
        if (DateToTimestamp(currentYear, currentMonth, 1, 0, 0, 0) > endTimestamp) {
            break;
        }
    }
}

bool ICSStreamParser::ParseEvent(const char* eventStart, const char* eventEnd,
                                  CalendarEvent* outEvent)
{
    // Get SUMMARY (title)
    if (!FindProperty(eventStart, eventEnd, "SUMMARY", outEvent->title, sizeof(outEvent->title))) {
        return false;  // Skip events without a title
    }

    // Get DTSTART
    char dtstart[64] = {0};
    if (FindProperty(eventStart, eventEnd, "DTSTART", dtstart, sizeof(dtstart))) {
        bool isAllDay = false;
        outEvent->startTime = ParseDateTime(dtstart, &isAllDay);
        outEvent->allDay = isAllDay;
    } else {
        return false;  // Skip events without a start time
    }

    // Get DTEND (optional)
    char dtend[64] = {0};
    if (FindProperty(eventStart, eventEnd, "DTEND", dtend, sizeof(dtend))) {
        bool dummy;
        outEvent->endTime = ParseDateTime(dtend, &dummy);
    } else {
        outEvent->endTime = outEvent->startTime;
    }

    // Copy calendar color as default
    strncpy(outEvent->calendarColor, m_calendarColor, sizeof(outEvent->calendarColor) - 1);
    outEvent->eventColor[0] = '\0';

    return true;
}

// Get Nth weekday of a month (for DST calculation)
// week: 1-4 for 1st-4th, 5 for last
// weekday: 0=Sunday
static unsigned GetNthWeekdayOfMonth(unsigned year, unsigned month, unsigned week, unsigned weekday)
{
    static const unsigned daysInMonth[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};
    bool isLeap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
    unsigned maxDay = daysInMonth[month - 1];
    if (month == 2 && isLeap) maxDay = 29;

    // Zeller's formula for day of week of 1st of month
    unsigned y = year, m = month;
    if (m < 3) { m += 12; y--; }
    unsigned k = y % 100, j = y / 100;
    unsigned h = (1 + (13 * (m + 1)) / 5 + k + k / 4 + j / 4 - 2 * j) % 7;
    unsigned firstDayOfWeek = (h + 6) % 7;  // Convert to 0=Sunday

    int firstOccurrence = 1 + ((int)weekday - (int)firstDayOfWeek + 7) % 7;
    unsigned day;
    if (week == 5) {
        day = firstOccurrence + 21;
        if (day + 7 <= maxDay) day += 7;
    } else {
        day = firstOccurrence + (week - 1) * 7;
    }
    return day;
}

// Check if a date falls in US DST (2nd Sunday March - 1st Sunday November)
static bool IsInUsDst(unsigned year, unsigned month, unsigned day)
{
    if (month < 3 || month > 11) return false;
    if (month > 3 && month < 11) return true;

    unsigned dstStart = GetNthWeekdayOfMonth(year, 3, 2, 0);  // 2nd Sunday March
    unsigned dstEnd = GetNthWeekdayOfMonth(year, 11, 1, 0);   // 1st Sunday November

    if (month == 3) return day >= dstStart;
    if (month == 11) return day < dstEnd;
    return false;
}

// Check if a date falls in EU DST (last Sunday March - last Sunday October)
static bool IsInEuDst(unsigned year, unsigned month, unsigned day)
{
    if (month < 3 || month > 10) return false;
    if (month > 3 && month < 10) return true;

    unsigned dstStart = GetNthWeekdayOfMonth(year, 3, 5, 0);  // Last Sunday March
    unsigned dstEnd = GetNthWeekdayOfMonth(year, 10, 5, 0);   // Last Sunday October

    if (month == 3) return day >= dstStart;
    if (month == 10) return day < dstEnd;
    return false;
}

// Parse timezone name to UTC offset, accounting for DST based on the event date
static int ParseTimezoneOffset(const char* tzName, unsigned year, unsigned month, unsigned day)
{
    // US abbreviated timezones (exact matches - these are fixed offsets)
    if (strcmp(tzName, "EST") == 0) return -5 * 3600;
    if (strcmp(tzName, "CST") == 0) return -6 * 3600;
    if (strcmp(tzName, "MST") == 0) return -7 * 3600;
    if (strcmp(tzName, "PST") == 0) return -8 * 3600;
    if (strcmp(tzName, "AKST") == 0) return -9 * 3600;
    if (strcmp(tzName, "HST") == 0) return -10 * 3600;
    if (strcmp(tzName, "EDT") == 0) return -4 * 3600;
    if (strcmp(tzName, "CDT") == 0) return -5 * 3600;
    if (strcmp(tzName, "MDT") == 0) return -6 * 3600;
    if (strcmp(tzName, "PDT") == 0) return -7 * 3600;
    if (strcmp(tzName, "AKDT") == 0) return -8 * 3600;

    // US timezones with DST
    bool usDst = IsInUsDst(year, month, day);
    if (strstr(tzName, "Eastern") || strstr(tzName, "America/New_York") ||
        strstr(tzName, "US/Eastern")) {
        return usDst ? -4 * 3600 : -5 * 3600;
    }
    if (strstr(tzName, "Central") || strstr(tzName, "America/Chicago") ||
        strstr(tzName, "US/Central")) {
        return usDst ? -5 * 3600 : -6 * 3600;
    }
    if (strstr(tzName, "Mountain") || strstr(tzName, "America/Denver") ||
        strstr(tzName, "US/Mountain")) {
        return usDst ? -6 * 3600 : -7 * 3600;
    }
    if (strstr(tzName, "Pacific") || strstr(tzName, "America/Los_Angeles") ||
        strstr(tzName, "US/Pacific")) {
        return usDst ? -7 * 3600 : -8 * 3600;
    }
    if (strstr(tzName, "Alaska") || strstr(tzName, "America/Anchorage")) {
        return usDst ? -8 * 3600 : -9 * 3600;
    }
    // No DST
    if (strstr(tzName, "Arizona") || strstr(tzName, "America/Phoenix")) {
        return -7 * 3600;
    }
    if (strstr(tzName, "Hawaii") || strstr(tzName, "Pacific/Honolulu")) {
        return -10 * 3600;
    }

    // Europe timezones with DST
    bool euDst = IsInEuDst(year, month, day);
    if (strcmp(tzName, "GMT") == 0) return 0;
    if (strcmp(tzName, "BST") == 0) return 1 * 3600;
    if (strstr(tzName, "Europe/London")) {
        return euDst ? 1 * 3600 : 0;
    }
    if (strcmp(tzName, "CET") == 0) return 1 * 3600;
    if (strcmp(tzName, "CEST") == 0) return 2 * 3600;
    if (strstr(tzName, "Europe/Paris") || strstr(tzName, "Europe/Berlin")) {
        return euDst ? 2 * 3600 : 1 * 3600;
    }

    // Default: assume UTC if unknown
    return 0;
}

unsigned ICSStreamParser::ParseDateTime(const char* value, bool* isAllDay)
{
    *isAllDay = false;
    bool isUTC = false;
    bool hasTZID = false;
    char tzName[64] = {0};

    // Extract TZID if present: "DTSTART;TZID=America/Chicago:20260115T100000"
    const char* tzidStart = strstr(value, "TZID=");
    if (tzidStart) {
        tzidStart += 5;  // Skip "TZID="
        const char* tzidEnd = strchr(tzidStart, ':');
        if (tzidEnd) {
            int tzLen = tzidEnd - tzidStart;
            if (tzLen > 63) tzLen = 63;
            strncpy(tzName, tzidStart, tzLen);
            tzName[tzLen] = '\0';
            hasTZID = true;
        }
    }

    // Handle parameters - find the actual datetime value after ':'
    const char* colonPos = strchr(value, ':');
    if (colonPos) {
        // Check for VALUE=DATE before colon (indicates all-day)
        const char* valueDate = strstr(value, "VALUE=DATE");
        if (valueDate && valueDate < colonPos) {
            *isAllDay = true;
        }
        value = colonPos + 1;
    }

    int len = strlen(value);

    // Check if time ends with 'Z' (UTC)
    if (len > 0 && value[len - 1] == 'Z') {
        isUTC = true;
    }

    // Date only format: YYYYMMDD (8 chars) - no 'T' means all-day
    if (len == 8 || (len > 8 && value[8] != 'T')) {
        *isAllDay = true;

        int year, month, day;
        if (sscanf(value, "%4d%2d%2d", &year, &month, &day) != 3) {
            return 0;
        }

        // All-day events: store as midnight UTC on that date
        unsigned days = 0;
        for (int y = 1970; y < year; y++) {
            bool leap = (y % 4 == 0 && y % 100 != 0) || (y % 400 == 0);
            days += leap ? 366 : 365;
        }

        static const int daysInMonth[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};
        bool leap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
        for (int m = 0; m < month - 1; m++) {
            days += daysInMonth[m];
            if (m == 1 && leap) days++;
        }
        days += day - 1;

        return days * 86400;
    }

    // DateTime format: YYYYMMDDTHHMMSS (15+ chars)
    if (len >= 15) {
        int year, month, day, hour, min, sec;
        if (sscanf(value, "%4d%2d%2dT%2d%2d%2d", &year, &month, &day, &hour, &min, &sec) != 6) {
            return 0;
        }

        unsigned days = 0;
        for (int y = 1970; y < year; y++) {
            bool leap = (y % 4 == 0 && y % 100 != 0) || (y % 400 == 0);
            days += leap ? 366 : 365;
        }

        static const int daysInMonth[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};
        bool leap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
        for (int m = 0; m < month - 1; m++) {
            days += daysInMonth[m];
            if (m == 1 && leap) days++;
        }
        days += day - 1;

        unsigned timestamp = days * 86400 + hour * 3600 + min * 60 + sec;

        // Convert to UTC:
        // - Z suffix: already UTC
        // - TZID present: convert from that timezone to UTC (subtract offset)
        // - Neither: assume UTC
        if (!isUTC && hasTZID) {
            // Get offset based on event date (handles DST)
            int tzOffset = ParseTimezoneOffset(tzName, year, month, day);
            // Time is in local timezone, convert to UTC
            timestamp = (unsigned)((int)timestamp - tzOffset);
        }

        return timestamp;
    }

    return 0;
}

bool ICSStreamParser::FindProperty(const char* block, const char* blockEnd,
                                    const char* propName, char* outValue, int maxLen)
{
    const char* pos = block;
    int propLen = strlen(propName);

    while (pos < blockEnd) {
        // Skip to start of line (or start of block)

        // Check if this line starts with the property name
        if (strncmp(pos, propName, propLen) == 0) {
            const char* afterProp = pos + propLen;
            if (*afterProp == ':' || *afterProp == ';') {
                // For properties with params (like DTSTART;TZID=...:value)
                // we want to include the params for datetime parsing
                if (*afterProp == ';') {
                    // Copy from after property name to end of line
                    const char* eol = afterProp;
                    while (eol < blockEnd && *eol != '\r' && *eol != '\n') {
                        eol++;
                    }
                    int valueLen = eol - afterProp;
                    if (valueLen >= maxLen) valueLen = maxLen - 1;
                    strncpy(outValue, afterProp, valueLen);
                    outValue[valueLen] = '\0';
                    return true;
                }

                // Simple case: PROPNAME:value
                const char* colon = afterProp;
                if (*colon == ':') {
                    colon++;
                    const char* eol = colon;
                    while (eol < blockEnd && *eol != '\r' && *eol != '\n') {
                        eol++;
                    }
                    int valueLen = eol - colon;
                    if (valueLen >= maxLen) valueLen = maxLen - 1;
                    strncpy(outValue, colon, valueLen);
                    outValue[valueLen] = '\0';
                    return true;
                }
            }
        }

        // Move to next line
        while (pos < blockEnd && *pos != '\n') {
            pos++;
        }
        if (pos < blockEnd) {
            pos++;  // Skip newline
        }
    }

    return false;
}

} // namespace mm
