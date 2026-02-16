#ifndef CALENDAR_WIDGET_H
#define CALENDAR_WIDGET_H

#include "modules/widgets/widget_base.h"

namespace mm {

// Calendar event structure
struct CalendarEvent {
    char title[64];
    unsigned startTime;     // Unix timestamp
    unsigned endTime;       // Unix timestamp
    char eventColor[8];     // Event-specific color (from ICS) or empty
    char calendarColor[8];  // Calendar default color like "#FF0000"
    bool allDay;
};

class CalendarWidget : public WidgetBase
{
public:
    CalendarWidget(lv_obj_t* parent, CTimer* timer);
    ~CalendarWidget() override;

    bool Initialize() override;
    void Update() override;

    // Set timezone name (e.g., "America/Chicago")
    void SetTimezone(const char* tzName);

    // Add events
    void ClearEvents();
    void AddEvent(const CalendarEvent& event);

    // Force re-render (call after updating events)
    void Refresh();

private:
    void CreateUI();
    void UpdateCalendar();
    void RenderRollingCalendar();

    // Helper functions
    unsigned GetDaysInMonth(unsigned year, unsigned month);
    unsigned GetDayOfWeek(unsigned year, unsigned month, unsigned day);
    int GetEventsForDay(unsigned year, unsigned month, unsigned day,
                        CalendarEvent* outEvents, int maxEvents);

    // Color helpers
    lv_color_t ParseHexColor(const char* hex);
    bool UseDarkText(lv_color_t bgColor);
    void FormatShortTime(unsigned unixTime, char* buffer, int bufSize);

    // Event rendering
    void RenderDayEvents(lv_obj_t* cell, int row, int col,
                         unsigned year, unsigned month, unsigned day);
    void CreateEventSlots(lv_obj_t* cell, int row, int col);

    // LVGL objects
    lv_obj_t*   m_pCalendarGrid;
    lv_obj_t*   m_pDayLabels[7];         // Sun-Sat headers
    lv_obj_t*   m_pDayCells[4][7];       // 4 weeks x 7 days - containers for day content
    lv_obj_t*   m_pDayNumbers[4][7];     // Day number labels

    // Pre-allocated event slots per day cell (avoids LVGL memory fragmentation)
    static const int MAX_EVENTS_PER_DAY = 4;
    struct EventSlot {
        lv_obj_t* container;  // Row container (used for timed events)
        lv_obj_t* bullet;     // Color bullet (hidden for all-day)
        lv_obj_t* label;      // Event text label
    };
    EventSlot   m_eventSlots[4][7][MAX_EVENTS_PER_DAY];

    // State
    char        m_timezone[64];
    unsigned    m_currentYear;
    unsigned    m_currentMonth;
    unsigned    m_currentDay;
    unsigned    m_lastUpdateDay;

    // Events
    static const int MAX_EVENTS = 200;
    CalendarEvent m_events[MAX_EVENTS];
    int         m_eventCount;

    // Buffer
    char        m_monthBuffer[32];
};

} // namespace mm

#endif // CALENDAR_WIDGET_H
