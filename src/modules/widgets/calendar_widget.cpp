#include "modules/widgets/calendar_widget.h"
#include "config/config.h"
#include <circle/util.h>
#include <circle/string.h>

namespace mm {

// Max events to display per day cell
static const int MAX_EVENTS_PER_DAY = 4;

CalendarWidget::CalendarWidget(lv_obj_t* parent, CTimer* timer)
    : WidgetBase("Calendar", parent, timer),
      m_pCalendarGrid(nullptr),
      m_currentYear(2026),
      m_currentMonth(1),
      m_currentDay(1),
      m_lastUpdateDay(0),
      m_eventCount(0)
{
    memset(m_pDayLabels, 0, sizeof(m_pDayLabels));
    memset(m_pDayCells, 0, sizeof(m_pDayCells));
    memset(m_pDayNumbers, 0, sizeof(m_pDayNumbers));
    memset(m_events, 0, sizeof(m_events));
    strcpy(m_timezone, "UTC");  // Default
}

void CalendarWidget::SetTimezone(const char* tzName)
{
    strncpy(m_timezone, tzName, sizeof(m_timezone) - 1);
    m_timezone[sizeof(m_timezone) - 1] = '\0';
}

CalendarWidget::~CalendarWidget()
{
    // LVGL objects are children of m_pContainer and will be deleted with it
}

bool CalendarWidget::Initialize()
{
    CreateUI();
    UpdateCalendar();
    return true;
}

void CalendarWidget::CreateUI()
{
    // Calendar grid container
    m_pCalendarGrid = lv_obj_create(m_pContainer);
    lv_obj_set_style_bg_opa(m_pCalendarGrid, LV_OPA_TRANSP, LV_PART_MAIN);
    lv_obj_set_style_border_width(m_pCalendarGrid, 0, LV_PART_MAIN);
    lv_obj_set_style_pad_all(m_pCalendarGrid, 0, LV_PART_MAIN);
    lv_obj_clear_flag(m_pCalendarGrid, LV_OBJ_FLAG_SCROLLABLE);
    lv_obj_set_size(m_pCalendarGrid, lv_pct(100), m_height);
    lv_obj_align(m_pCalendarGrid, LV_ALIGN_TOP_LEFT, 0, 0);

    // Calculate cell size - minimal header height, rest for 4 weeks
    int cellWidth = m_width / 7;
    int headerHeight = 20;
    int cellHeight = (m_height - headerHeight) / 4;

    // Day headers (Sun-Sat)
    static const char* dayHeaders[] = {"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"};
    for (int i = 0; i < 7; i++) {
        m_pDayLabels[i] = lv_label_create(m_pCalendarGrid);
        lv_obj_set_style_text_color(m_pDayLabels[i], lv_color_make(150, 150, 150), LV_PART_MAIN);
        lv_obj_set_style_text_font(m_pDayLabels[i], &lv_font_montserrat_22, LV_PART_MAIN);
        lv_obj_set_pos(m_pDayLabels[i], i * cellWidth, 0);
        lv_label_set_text(m_pDayLabels[i], dayHeaders[i]);
    }

    // Day cells (4 rows x 7 columns) - each is a container
    for (int row = 0; row < 4; row++) {
        for (int col = 0; col < 7; col++) {
            // Create cell container
            m_pDayCells[row][col] = lv_obj_create(m_pCalendarGrid);
            lv_obj_set_style_bg_opa(m_pDayCells[row][col], LV_OPA_TRANSP, LV_PART_MAIN);
            lv_obj_set_style_border_width(m_pDayCells[row][col], 0, LV_PART_MAIN);
            lv_obj_set_style_pad_all(m_pDayCells[row][col], 6, LV_PART_MAIN);
            lv_obj_set_style_pad_row(m_pDayCells[row][col], 6, LV_PART_MAIN);
            lv_obj_clear_flag(m_pDayCells[row][col], LV_OBJ_FLAG_SCROLLABLE);
            lv_obj_set_size(m_pDayCells[row][col], cellWidth, cellHeight);
            lv_obj_set_pos(m_pDayCells[row][col], col * cellWidth, headerHeight + row * cellHeight);

            // Use flex layout for stacking events
            lv_obj_set_layout(m_pDayCells[row][col], LV_LAYOUT_FLEX);
            lv_obj_set_flex_flow(m_pDayCells[row][col], LV_FLEX_FLOW_COLUMN);

            // Day number label (first child in each cell)
            m_pDayNumbers[row][col] = lv_label_create(m_pDayCells[row][col]);
            lv_obj_set_style_text_color(m_pDayNumbers[row][col], lv_color_make(180, 180, 180), LV_PART_MAIN);
            lv_obj_set_style_text_font(m_pDayNumbers[row][col], &lv_font_montserrat_22, LV_PART_MAIN);
            lv_label_set_text(m_pDayNumbers[row][col], "");
        }
    }
}

void CalendarWidget::Update()
{
    // Calculate current date from timer (using timezone with DST)
    unsigned utcTime = m_pTimer->GetTime();
    int offset = GetTimezoneOffset(m_timezone, utcTime);
    unsigned localTime = (unsigned)((int)utcTime + offset);
    unsigned days = localTime / 86400;

    // Only update if day changed
    if (days != m_lastUpdateDay) {
        m_lastUpdateDay = days;
        UpdateCalendar();
    }
}

void CalendarWidget::UpdateCalendar()
{
    // Calculate current date (using timezone with DST)
    unsigned utcTime = m_pTimer->GetTime();
    int offset = GetTimezoneOffset(m_timezone, utcTime);
    unsigned localTime = (unsigned)((int)utcTime + offset);
    unsigned days = localTime / 86400;

    // Calculate year, month, day
    unsigned year = 1970;
    static const unsigned daysInMonth[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};

    unsigned remainingDays = days;

    // Find year
    while (true) {
        bool isLeap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
        unsigned daysInYear = isLeap ? 366 : 365;
        if (remainingDays < daysInYear) {
            break;
        }
        remainingDays -= daysInYear;
        year++;
    }

    // Find month
    unsigned month = 0;
    bool isLeap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
    for (month = 0; month < 12; month++) {
        unsigned dim = daysInMonth[month];
        if (month == 1 && isLeap) {
            dim = 29;
        }
        if (remainingDays < dim) {
            break;
        }
        remainingDays -= dim;
    }

    m_currentYear = year;
    m_currentMonth = month + 1;  // 1-indexed
    m_currentDay = remainingDays + 1;

    RenderRollingCalendar();
}

void CalendarWidget::RenderRollingCalendar()
{
    static const char* monthShort[] = {
        "Jan", "Feb", "Mar", "Apr", "May", "Jun",
        "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"
    };

    // Find the Sunday of the current week (start of rolling calendar)
    unsigned currentDayOfWeek = GetDayOfWeek(m_currentYear, m_currentMonth, m_currentDay);

    // Calculate the starting date (Sunday of current week)
    int startDay = m_currentDay - currentDayOfWeek;
    int startMonth = m_currentMonth;
    int startYear = m_currentYear;

    // Handle going back to previous month
    while (startDay < 1) {
        startMonth--;
        if (startMonth < 1) {
            startMonth = 12;
            startYear--;
        }
        startDay += GetDaysInMonth(startYear, startMonth);
    }

    // Fill in the 4 weeks
    int day = startDay;
    int month = startMonth;
    int year = startYear;

    for (int row = 0; row < 4; row++) {
        for (int col = 0; col < 7; col++) {
            // Clear old events from cell
            ClearDayEvents(m_pDayCells[row][col]);

            CString dayStr;

            // First day of month: show "Mon D" format
            if (day == 1) {
                dayStr.Format("%s %d", monthShort[month - 1], day);
            } else {
                dayStr.Format("%d", day);
            }

            lv_label_set_text(m_pDayNumbers[row][col], (const char*)dayStr);

            // Highlight current day with darker background and blue text
            bool isToday = (day == (int)m_currentDay && month == (int)m_currentMonth && year == (int)m_currentYear);
            if (isToday) {
                lv_obj_set_style_bg_color(m_pDayCells[row][col], lv_color_make(50, 50, 55), LV_PART_MAIN);
                lv_obj_set_style_bg_opa(m_pDayCells[row][col], LV_OPA_COVER, LV_PART_MAIN);
                lv_obj_set_style_text_color(m_pDayNumbers[row][col],
                                           lv_color_make(100, 200, 255), LV_PART_MAIN);
            } else {
                lv_obj_set_style_bg_opa(m_pDayCells[row][col], LV_OPA_TRANSP, LV_PART_MAIN);
                lv_obj_set_style_text_color(m_pDayNumbers[row][col],
                                           lv_color_make(180, 180, 180), LV_PART_MAIN);
            }

            // Render events for this day
            RenderDayEvents(m_pDayCells[row][col], year, month, day);

            // Move to next day
            day++;
            unsigned daysInMonth = GetDaysInMonth(year, month);
            if (day > (int)daysInMonth) {
                day = 1;
                month++;
                if (month > 12) {
                    month = 1;
                    year++;
                }
            }
        }
    }
}

lv_color_t CalendarWidget::ParseHexColor(const char* hex)
{
    if (!hex || hex[0] != '#' || strlen(hex) < 7) {
        return lv_color_make(100, 100, 100);  // Default gray
    }

    unsigned int r, g, b;
    // Parse hex color like "#FF0000"
    char rStr[3] = {hex[1], hex[2], 0};
    char gStr[3] = {hex[3], hex[4], 0};
    char bStr[3] = {hex[5], hex[6], 0};

    r = (unsigned int)strtoul(rStr, nullptr, 16);
    g = (unsigned int)strtoul(gStr, nullptr, 16);
    b = (unsigned int)strtoul(bStr, nullptr, 16);

    return lv_color_make(r, g, b);
}

bool CalendarWidget::UseDarkText(lv_color_t bgColor)
{
    // Calculate relative luminance using sRGB
    // Formula: 0.299*R + 0.587*G + 0.114*B
    int r = bgColor.red;
    int g = bgColor.green;
    int b = bgColor.blue;

    int luminance = (299 * r + 587 * g + 114 * b) / 1000;

    // Use dark text if background is light (luminance > 128)
    return luminance > 128;
}

void CalendarWidget::FormatShortTime(unsigned unixTime, char* buffer, int bufSize)
{
    // Convert UTC to local time for display (handles DST)
    int offset = GetTimezoneOffset(m_timezone, unixTime);
    unsigned localTime = (unsigned)((int)unixTime + offset);

    // Get hours and minutes
    unsigned secondsInDay = localTime % 86400;
    unsigned hour = secondsInDay / 3600;
    unsigned minute = (secondsInDay % 3600) / 60;

    // Convert to 12-hour format
    const char* ampm = (hour < 12) ? "am" : "pm";
    if (hour == 0) hour = 12;
    else if (hour > 12) hour -= 12;

    CString timeStr;
    if (minute == 0) {
        timeStr.Format("%d%s", hour, ampm);
    } else {
        timeStr.Format("%d:%02d%s", hour, minute, ampm);
    }

    strncpy(buffer, (const char*)timeStr, bufSize - 1);
    buffer[bufSize - 1] = '\0';
}

int CalendarWidget::GetEventsForDay(unsigned year, unsigned month, unsigned day,
                                     CalendarEvent* outEvents, int maxEvents)
{
    int count = 0;
    static const unsigned daysInMonthArr[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};

    for (int i = 0; i < m_eventCount && count < maxEvents; i++) {
        // For all-day events, don't apply timezone - they're dates, not timestamps
        // For timed events, convert UTC to local time (handles DST)
        unsigned eventDays;
        if (m_events[i].allDay) {
            // All-day events are stored as midnight UTC on the target date
            eventDays = m_events[i].startTime / 86400;
        } else {
            int offset = GetTimezoneOffset(m_timezone, m_events[i].startTime);
            unsigned localTime = (unsigned)((int)m_events[i].startTime + offset);
            eventDays = localTime / 86400;
        }

        // Calculate event date
        unsigned eventYear = 1970;
        unsigned remainingDays = eventDays;

        while (true) {
            bool isLeap = (eventYear % 4 == 0 && eventYear % 100 != 0) || (eventYear % 400 == 0);
            unsigned daysInYear = isLeap ? 366 : 365;
            if (remainingDays < daysInYear) {
                break;
            }
            remainingDays -= daysInYear;
            eventYear++;
        }

        unsigned eventMonth = 0;
        bool isLeap = (eventYear % 4 == 0 && eventYear % 100 != 0) || (eventYear % 400 == 0);
        for (eventMonth = 0; eventMonth < 12; eventMonth++) {
            unsigned dim = daysInMonthArr[eventMonth];
            if (eventMonth == 1 && isLeap) {
                dim = 29;
            }
            if (remainingDays < dim) {
                break;
            }
            remainingDays -= dim;
        }

        unsigned eventDay = remainingDays + 1;

        if (eventYear == year && eventMonth + 1 == month && eventDay == day) {
            outEvents[count++] = m_events[i];
        }
    }

    // Sort: all-day events first, then by start time
    for (int i = 0; i < count - 1; i++) {
        for (int j = i + 1; j < count; j++) {
            bool swap = false;
            if (outEvents[j].allDay && !outEvents[i].allDay) {
                swap = true;
            } else if (outEvents[i].allDay == outEvents[j].allDay) {
                if (outEvents[j].startTime < outEvents[i].startTime) {
                    swap = true;
                }
            }
            if (swap) {
                CalendarEvent temp = outEvents[i];
                outEvents[i] = outEvents[j];
                outEvents[j] = temp;
            }
        }
    }

    return count;
}

void CalendarWidget::ClearDayEvents(lv_obj_t* cell)
{
    // Delete all children except the first one (day number label)
    uint32_t childCount = lv_obj_get_child_count(cell);
    for (int i = childCount - 1; i > 0; i--) {
        lv_obj_t* child = lv_obj_get_child(cell, i);
        if (child) {
            lv_obj_delete(child);
        }
    }
}

void CalendarWidget::RenderDayEvents(lv_obj_t* cell, unsigned year, unsigned month, unsigned day)
{
    CalendarEvent dayEvents[MAX_EVENTS_PER_DAY];
    int eventCount = GetEventsForDay(year, month, day, dayEvents, MAX_EVENTS_PER_DAY);

    for (int i = 0; i < eventCount; i++) {
        const CalendarEvent& event = dayEvents[i];

        // Get event color (prefer event color, fallback to calendar color)
        const char* colorStr = (event.eventColor[0] != '\0') ? event.eventColor : event.calendarColor;
        lv_color_t eventColor = ParseHexColor(colorStr);

        if (event.allDay) {
            // All-day event: colored background with contrast text
            lv_obj_t* eventLabel = lv_label_create(cell);
            lv_obj_set_width(eventLabel, lv_pct(100));
            lv_obj_set_style_bg_color(eventLabel, eventColor, LV_PART_MAIN);
            lv_obj_set_style_bg_opa(eventLabel, LV_OPA_COVER, LV_PART_MAIN);
            lv_obj_set_style_pad_left(eventLabel, 4, LV_PART_MAIN);
            lv_obj_set_style_pad_right(eventLabel, 4, LV_PART_MAIN);
            lv_obj_set_style_pad_top(eventLabel, 4, LV_PART_MAIN);
            lv_obj_set_style_pad_bottom(eventLabel, 4, LV_PART_MAIN);
            lv_obj_set_style_radius(eventLabel, 3, LV_PART_MAIN);

            // Choose text color based on background brightness
            if (UseDarkText(eventColor)) {
                lv_obj_set_style_text_color(eventLabel, lv_color_make(0, 0, 0), LV_PART_MAIN);
            } else {
                lv_obj_set_style_text_color(eventLabel, lv_color_make(255, 255, 255), LV_PART_MAIN);
            }

            lv_obj_set_style_text_font(eventLabel, &lv_font_montserrat_16, LV_PART_MAIN);
            lv_label_set_long_mode(eventLabel, LV_LABEL_LONG_DOT);
            lv_obj_set_style_max_height(eventLabel, 28, LV_PART_MAIN);  // Single line + padding for 16pt
            lv_label_set_text(eventLabel, event.title);
        } else {
            // Timed event: bullet + time + title (wraps to 2 lines max)
            lv_obj_t* eventRow = lv_obj_create(cell);
            lv_obj_set_width(eventRow, lv_pct(100));
            lv_obj_set_height(eventRow, LV_SIZE_CONTENT);
            lv_obj_set_style_bg_opa(eventRow, LV_OPA_TRANSP, LV_PART_MAIN);
            lv_obj_set_style_border_width(eventRow, 0, LV_PART_MAIN);
            lv_obj_set_style_pad_all(eventRow, 0, LV_PART_MAIN);
            lv_obj_clear_flag(eventRow, LV_OBJ_FLAG_SCROLLABLE);
            lv_obj_set_layout(eventRow, LV_LAYOUT_FLEX);
            lv_obj_set_flex_flow(eventRow, LV_FLEX_FLOW_ROW);
            lv_obj_set_style_pad_column(eventRow, 4, LV_PART_MAIN);
            lv_obj_set_flex_align(eventRow, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_START);

            // Color bullet (match font height ~14px)
            lv_obj_t* bullet = lv_obj_create(eventRow);
            lv_obj_set_size(bullet, 12, 12);
            lv_obj_set_style_bg_color(bullet, eventColor, LV_PART_MAIN);
            lv_obj_set_style_bg_opa(bullet, LV_OPA_COVER, LV_PART_MAIN);
            lv_obj_set_style_border_width(bullet, 0, LV_PART_MAIN);
            lv_obj_set_style_radius(bullet, 2, LV_PART_MAIN);
            lv_obj_clear_flag(bullet, LV_OBJ_FLAG_SCROLLABLE);

            // Format time and title
            char timeStr[16];
            FormatShortTime(event.startTime, timeStr, sizeof(timeStr));

            CString eventText;
            eventText.Format("%s %s", timeStr, event.title);

            lv_obj_t* eventLabel = lv_label_create(eventRow);
            lv_obj_set_flex_grow(eventLabel, 1);
            lv_obj_set_style_text_color(eventLabel, lv_color_make(200, 200, 200), LV_PART_MAIN);
            lv_obj_set_style_text_font(eventLabel, &lv_font_montserrat_16, LV_PART_MAIN);
            lv_label_set_long_mode(eventLabel, LV_LABEL_LONG_WRAP);
            lv_obj_set_style_max_height(eventLabel, 40, LV_PART_MAIN);  // ~2 lines at 16px
            lv_label_set_text(eventLabel, (const char*)eventText);
        }
    }
}

unsigned CalendarWidget::GetDaysInMonth(unsigned year, unsigned month)
{
    static const unsigned daysInMonth[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};
    bool isLeap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);

    if (month == 2 && isLeap) {
        return 29;
    }
    return daysInMonth[month - 1];
}

unsigned CalendarWidget::GetDayOfWeek(unsigned year, unsigned month, unsigned day)
{
    // Zeller's formula (modified for Sunday = 0)
    if (month < 3) {
        month += 12;
        year--;
    }

    int k = year % 100;
    int j = year / 100;

    int h = (day + (13 * (month + 1)) / 5 + k + k / 4 + j / 4 - 2 * j) % 7;

    // Convert from Zeller (Saturday = 0) to Sunday = 0
    return (h + 6) % 7;
}

void CalendarWidget::ClearEvents()
{
    m_eventCount = 0;
    memset(m_events, 0, sizeof(m_events));
}

void CalendarWidget::AddEvent(const CalendarEvent& event)
{
    if (m_eventCount < MAX_EVENTS) {
        m_events[m_eventCount++] = event;
    }
}

void CalendarWidget::Refresh()
{
    // Force re-render by resetting last update day
    m_lastUpdateDay = 0;
    UpdateCalendar();
}

} // namespace mm
