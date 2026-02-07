#include "modules/widgets/upcoming_events_widget.h"
#include "config/config.h"
#include <circle/util.h>
#include <circle/string.h>

namespace mm {

UpcomingEventsWidget::UpcomingEventsWidget(lv_obj_t* parent, CTimer* timer)
    : WidgetBase("UpcomingEvents", parent, timer),
      m_pHeader(nullptr),
      m_pEventList(nullptr),
      m_maxEvents(10),
      m_eventCount(0),
      m_lastRenderDay(0)
{
    memset(m_events, 0, sizeof(m_events));
    strcpy(m_timezone, "UTC");
}

UpcomingEventsWidget::~UpcomingEventsWidget()
{
    // LVGL objects are children of m_pContainer and will be deleted with it
}

void UpcomingEventsWidget::SetTimezone(const char* tzName)
{
    strncpy(m_timezone, tzName, sizeof(m_timezone) - 1);
    m_timezone[sizeof(m_timezone) - 1] = '\0';
}

bool UpcomingEventsWidget::Initialize()
{
    CreateUI();
    RenderEvents();
    return true;
}

void UpcomingEventsWidget::CreateUI()
{
    // Set container to use flex column layout for stacking header + list
    lv_obj_set_flex_flow(m_pContainer, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_row(m_pContainer, 8, LV_PART_MAIN);

    // Header container with bottom border
    lv_obj_t* headerContainer = lv_obj_create(m_pContainer);
    lv_obj_set_width(headerContainer, lv_pct(100));
    lv_obj_set_height(headerContainer, LV_SIZE_CONTENT);
    lv_obj_set_style_bg_opa(headerContainer, LV_OPA_TRANSP, LV_PART_MAIN);
    lv_obj_set_style_border_width(headerContainer, 0, LV_PART_MAIN);
    lv_obj_set_style_border_side(headerContainer, LV_BORDER_SIDE_BOTTOM, LV_PART_MAIN);
    lv_obj_set_style_border_width(headerContainer, 1, LV_PART_MAIN);
    lv_obj_set_style_border_color(headerContainer, lv_color_make(60, 60, 60), LV_PART_MAIN);
    lv_obj_set_style_pad_bottom(headerContainer, 8, LV_PART_MAIN);
    lv_obj_set_style_pad_top(headerContainer, 0, LV_PART_MAIN);
    lv_obj_set_style_pad_left(headerContainer, 0, LV_PART_MAIN);
    lv_obj_clear_flag(headerContainer, LV_OBJ_FLAG_SCROLLABLE);

    // Header label "UPCOMING EVENTS" - gray text, no special color
    m_pHeader = lv_label_create(headerContainer);
    lv_obj_set_style_text_color(m_pHeader, lv_color_make(100, 100, 100), LV_PART_MAIN);
    lv_obj_set_style_text_font(m_pHeader, &lv_font_montserrat_14, LV_PART_MAIN);
    lv_label_set_text(m_pHeader, "UPCOMING EVENTS");

    // Event list container with flex column layout - fills remaining space
    m_pEventList = lv_obj_create(m_pContainer);
    lv_obj_set_style_bg_opa(m_pEventList, LV_OPA_TRANSP, LV_PART_MAIN);
    lv_obj_set_style_border_width(m_pEventList, 0, LV_PART_MAIN);
    lv_obj_set_style_pad_all(m_pEventList, 0, LV_PART_MAIN);
    lv_obj_set_style_pad_row(m_pEventList, 4, LV_PART_MAIN);
    lv_obj_clear_flag(m_pEventList, LV_OBJ_FLAG_SCROLLABLE);
    lv_obj_set_width(m_pEventList, lv_pct(100));
    lv_obj_set_flex_grow(m_pEventList, 1);  // Fill remaining vertical space
    lv_obj_set_layout(m_pEventList, LV_LAYOUT_FLEX);
    lv_obj_set_flex_flow(m_pEventList, LV_FLEX_FLOW_COLUMN);
}

void UpcomingEventsWidget::Update()
{
    // Calculate current day from timer
    unsigned utcTime = m_pTimer->GetTime();
    int offset = GetTimezoneOffset(m_timezone, utcTime);
    unsigned localTime = (unsigned)((int)utcTime + offset);
    unsigned days = localTime / 86400;

    // Re-render if day changed (to update "Today", "Tomorrow" labels)
    if (days != m_lastRenderDay) {
        m_lastRenderDay = days;
        RenderEvents();
    }
}

lv_color_t UpcomingEventsWidget::ParseHexColor(const char* hex)
{
    if (!hex || hex[0] != '#' || strlen(hex) < 7) {
        return lv_color_make(100, 100, 100);  // Default gray
    }

    unsigned int r, g, b;
    char rStr[3] = {hex[1], hex[2], 0};
    char gStr[3] = {hex[3], hex[4], 0};
    char bStr[3] = {hex[5], hex[6], 0};

    r = (unsigned int)strtoul(rStr, nullptr, 16);
    g = (unsigned int)strtoul(gStr, nullptr, 16);
    b = (unsigned int)strtoul(bStr, nullptr, 16);

    return lv_color_make(r, g, b);
}

void UpcomingEventsWidget::FormatEventDate(const CalendarEvent& event, unsigned now,
                                            char* buffer, int bufSize)
{
    // Get current local time
    int nowOffset = GetTimezoneOffset(m_timezone, now);
    unsigned nowLocal = (unsigned)((int)now + nowOffset);
    unsigned nowDays = nowLocal / 86400;

    // Get event local time
    unsigned eventTime;
    unsigned eventDays;

    if (event.allDay) {
        // All-day events are stored as midnight UTC on the target date
        eventDays = event.startTime / 86400;
        eventTime = event.startTime;
    } else {
        int eventOffset = GetTimezoneOffset(m_timezone, event.startTime);
        unsigned eventLocal = (unsigned)((int)event.startTime + eventOffset);
        eventDays = eventLocal / 86400;
        eventTime = eventLocal;
    }

    int daysDiff = (int)eventDays - (int)nowDays;

    if (daysDiff == 0) {
        // Today
        if (event.allDay) {
            strncpy(buffer, "Today", bufSize - 1);
        } else {
            unsigned secondsInDay = eventTime % 86400;
            unsigned hour = secondsInDay / 3600;
            unsigned minute = (secondsInDay % 3600) / 60;
            const char* ampm = (hour < 12) ? "PM" : "PM";
            if (hour == 0) { hour = 12; ampm = "AM"; }
            else if (hour < 12) { ampm = "AM"; }
            else if (hour == 12) { ampm = "PM"; }
            else { hour -= 12; ampm = "PM"; }

            CString timeStr;
            if (minute == 0) {
                timeStr.Format("Today at %d %s", hour, ampm);
            } else {
                timeStr.Format("Today at %d:%02d %s", hour, minute, ampm);
            }
            strncpy(buffer, (const char*)timeStr, bufSize - 1);
        }
    } else if (daysDiff == 1) {
        // Tomorrow
        if (event.allDay) {
            strncpy(buffer, "Tomorrow", bufSize - 1);
        } else {
            unsigned secondsInDay = eventTime % 86400;
            unsigned hour = secondsInDay / 3600;
            unsigned minute = (secondsInDay % 3600) / 60;
            const char* ampm = "PM";
            if (hour == 0) { hour = 12; ampm = "AM"; }
            else if (hour < 12) { ampm = "AM"; }
            else if (hour == 12) { ampm = "PM"; }
            else { hour -= 12; ampm = "PM"; }

            CString timeStr;
            if (minute == 0) {
                timeStr.Format("Tomorrow at %d %s", hour, ampm);
            } else {
                timeStr.Format("Tomorrow at %d:%02d %s", hour, minute, ampm);
            }
            strncpy(buffer, (const char*)timeStr, bufSize - 1);
        }
    } else if (daysDiff < 7) {
        // This week - show day name
        static const char* dayNames[] = {
            "Sunday", "Monday", "Tuesday", "Wednesday",
            "Thursday", "Friday", "Saturday"
        };

        // Calculate day of week from eventDays
        // Jan 1, 1970 was Thursday (4)
        int dow = (eventDays + 4) % 7;
        strncpy(buffer, dayNames[dow], bufSize - 1);
    } else {
        // Show month and day
        static const char* monthNames[] = {
            "Jan", "Feb", "Mar", "Apr", "May", "Jun",
            "Jul", "Aug", "Sep", "Oct", "Nov", "Dec"
        };
        static const unsigned daysInMonth[] = {31, 28, 31, 30, 31, 30, 31, 31, 30, 31, 30, 31};

        // Calculate year/month/day from eventDays
        unsigned year = 1970;
        unsigned remainingDays = eventDays;

        while (true) {
            bool isLeap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
            unsigned daysInYear = isLeap ? 366 : 365;
            if (remainingDays < daysInYear) break;
            remainingDays -= daysInYear;
            year++;
        }

        unsigned month = 0;
        bool isLeap = (year % 4 == 0 && year % 100 != 0) || (year % 400 == 0);
        for (month = 0; month < 12; month++) {
            unsigned dim = daysInMonth[month];
            if (month == 1 && isLeap) dim = 29;
            if (remainingDays < dim) break;
            remainingDays -= dim;
        }
        unsigned day = remainingDays + 1;

        // Format with ordinal suffix
        const char* suffix = "th";
        if (day == 1 || day == 21 || day == 31) suffix = "st";
        else if (day == 2 || day == 22) suffix = "nd";
        else if (day == 3 || day == 23) suffix = "rd";

        CString dateStr;
        dateStr.Format("%s %d%s", monthNames[month], day, suffix);
        strncpy(buffer, (const char*)dateStr, bufSize - 1);
    }

    buffer[bufSize - 1] = '\0';
}

void UpcomingEventsWidget::RenderEvents()
{
    // Clear existing event rows
    lv_obj_clean(m_pEventList);

    // Get current time
    unsigned now = m_pTimer->GetTime();

    // Get timezone offset for consistent comparisons
    int tzOffset = GetTimezoneOffset(m_timezone, now);

    // Helper lambda to get sortable time value for an event
    // All-day events use UTC days, timed events use local time
    auto getSortTime = [tzOffset](const CalendarEvent& event) -> unsigned {
        if (event.allDay) {
            // All-day events: stored as midnight UTC on the date
            // Return as-is for sorting - they sort at start of their UTC day
            return event.startTime;
        } else {
            // Timed events: convert to local time for proper day ordering
            return (unsigned)((int)event.startTime + tzOffset);
        }
    };

    // Get current local day for filtering
    unsigned nowLocal = (unsigned)((int)now + tzOffset);
    unsigned nowDays = nowLocal / 86400;

    // Sort events by date/time (ascending)
    // Simple insertion sort since we have limited events
    CalendarEvent sorted[MAX_EVENTS];
    int sortedCount = 0;

    // Only include events in the future
    for (int i = 0; i < m_eventCount; i++) {
        // Skip past events (with a bit of buffer for ongoing events)
        if (!m_events[i].allDay && m_events[i].startTime < now - 3600) {
            continue;  // Skip events more than 1 hour in the past
        }
        if (m_events[i].allDay) {
            // For all-day events, skip if the day has passed
            // All-day events are stored as midnight UTC on the date - use UTC days directly
            unsigned eventDays = m_events[i].startTime / 86400;
            if (eventDays < nowDays) continue;
        }

        // Get sortable time for this event
        unsigned sortTime = getSortTime(m_events[i]);

        // Insert in sorted order using sortable time
        int j = sortedCount;
        while (j > 0 && getSortTime(sorted[j-1]) > sortTime) {
            sorted[j] = sorted[j-1];
            j--;
        }
        sorted[j] = m_events[i];
        sortedCount++;

        if (sortedCount >= MAX_EVENTS) break;
    }

    // Calculate row height and visible count based on available space
    int rowHeight = 30;  // Sized for 18pt font with comfortable spacing
    int rowGap = 4;  // Matches pad_row set on m_pEventList
    int availableHeight = lv_obj_get_content_height(m_pEventList);
    int visibleRows = (availableHeight > 0) ? (availableHeight + rowGap) / (rowHeight + rowGap) : m_maxEvents;
    if (visibleRows > m_maxEvents) visibleRows = m_maxEvents;
    if (visibleRows < 1) visibleRows = 1;

    // Render each event
    int eventsToShow = (sortedCount < visibleRows) ? sortedCount : visibleRows;
    int fadeStartRow = eventsToShow - 3;  // Start fading 3 rows before end

    // Get container width for title sizing (container has its size set already)
    int containerWidth = lv_obj_get_content_width(m_pContainer);

    for (int i = 0; i < eventsToShow; i++) {
        const CalendarEvent& event = sorted[i];

        // Calculate opacity for fade effect
        int opacity = 255;
        if (i >= fadeStartRow && fadeStartRow >= 0) {
            opacity = 255 - (i - fadeStartRow) * 60;
            if (opacity < 80) opacity = 80;
        }

        // Create event row container
        lv_obj_t* row = lv_obj_create(m_pEventList);
        lv_obj_set_width(row, lv_pct(100));
        lv_obj_set_height(row, rowHeight);
        lv_obj_set_style_bg_opa(row, LV_OPA_TRANSP, LV_PART_MAIN);
        lv_obj_set_style_border_width(row, 0, LV_PART_MAIN);
        lv_obj_set_style_pad_all(row, 0, LV_PART_MAIN);
        lv_obj_clear_flag(row, LV_OBJ_FLAG_SCROLLABLE);

        // Calendar color indicator (small square)
        const char* colorStr = (event.eventColor[0] != '\0') ? event.eventColor : event.calendarColor;
        lv_color_t eventColor = ParseHexColor(colorStr);

        lv_obj_t* colorDot = lv_obj_create(row);
        lv_obj_set_size(colorDot, 8, 8);
        lv_obj_set_style_bg_color(colorDot, eventColor, LV_PART_MAIN);
        lv_obj_set_style_bg_opa(colorDot, opacity, LV_PART_MAIN);
        lv_obj_set_style_border_width(colorDot, 0, LV_PART_MAIN);
        lv_obj_set_style_radius(colorDot, 2, LV_PART_MAIN);
        lv_obj_clear_flag(colorDot, LV_OBJ_FLAG_SCROLLABLE);
        lv_obj_align(colorDot, LV_ALIGN_LEFT_MID, 0, 0);

        // Event title - use container width for sizing
        int titleWidth = containerWidth - 140;  // Leave room for color dot (16) + date (~120)
        if (titleWidth < 50) titleWidth = 50;  // Minimum width

        lv_obj_t* titleLabel = lv_label_create(row);
        lv_obj_set_style_text_color(titleLabel, lv_color_make(255, 255, 255), LV_PART_MAIN);
        lv_obj_set_style_text_opa(titleLabel, opacity, LV_PART_MAIN);
        lv_obj_set_style_text_font(titleLabel, &lv_font_montserrat_18, LV_PART_MAIN);
        lv_label_set_long_mode(titleLabel, LV_LABEL_LONG_DOT);
        lv_obj_set_width(titleLabel, titleWidth);
        lv_obj_set_style_max_height(titleLabel, 24, LV_PART_MAIN);  // Single line height for 18pt
        lv_label_set_text(titleLabel, event.title);
        lv_obj_align(titleLabel, LV_ALIGN_LEFT_MID, 16, 0);

        // Event date (right-aligned)
        char dateStr[32];
        FormatEventDate(event, now, dateStr, sizeof(dateStr));

        lv_obj_t* dateLabel = lv_label_create(row);
        lv_obj_set_style_text_color(dateLabel, lv_color_make(150, 150, 150), LV_PART_MAIN);
        lv_obj_set_style_text_opa(dateLabel, opacity, LV_PART_MAIN);
        lv_obj_set_style_text_font(dateLabel, &lv_font_montserrat_16, LV_PART_MAIN);
        lv_label_set_text(dateLabel, dateStr);
        lv_obj_align(dateLabel, LV_ALIGN_RIGHT_MID, 0, 0);
    }
}

void UpcomingEventsWidget::ClearEvents()
{
    m_eventCount = 0;
    memset(m_events, 0, sizeof(m_events));
}

void UpcomingEventsWidget::AddEvent(const CalendarEvent& event)
{
    if (m_eventCount < MAX_EVENTS) {
        m_events[m_eventCount++] = event;
    }
}

void UpcomingEventsWidget::Refresh()
{
    m_lastRenderDay = 0;  // Force re-render
    RenderEvents();
}

} // namespace mm
