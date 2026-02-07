#ifndef UPCOMING_EVENTS_WIDGET_H
#define UPCOMING_EVENTS_WIDGET_H

#include "modules/widgets/widget_base.h"
#include "modules/widgets/calendar_widget.h"  // For CalendarEvent

namespace mm {

class UpcomingEventsWidget : public WidgetBase
{
public:
    UpcomingEventsWidget(lv_obj_t* parent, CTimer* timer);
    ~UpcomingEventsWidget() override;

    bool Initialize() override;
    void Update() override;

    // Set timezone name (e.g., "America/Chicago")
    void SetTimezone(const char* tzName);

    // Events management (shares same CalendarEvent struct as CalendarWidget)
    void ClearEvents();
    void AddEvent(const CalendarEvent& event);
    void Refresh();

    // Configuration
    void SetMaxEvents(int max) { m_maxEvents = max; }

private:
    void CreateUI();
    void RenderEvents();

    // Format date/time for display
    void FormatEventDate(const CalendarEvent& event, unsigned now, char* buffer, int bufSize);

    // Parse hex color
    lv_color_t ParseHexColor(const char* hex);

    // LVGL objects
    lv_obj_t*   m_pHeader;
    lv_obj_t*   m_pEventList;

    // State
    char        m_timezone[64];
    int         m_maxEvents;

    // Events storage
    static const int MAX_EVENTS = 100;
    CalendarEvent m_events[MAX_EVENTS];
    int         m_eventCount;

    // Last render time for refresh detection
    unsigned    m_lastRenderDay;
};

} // namespace mm

#endif // UPCOMING_EVENTS_WIDGET_H
