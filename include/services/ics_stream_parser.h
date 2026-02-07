#ifndef ICS_STREAM_PARSER_H
#define ICS_STREAM_PARSER_H

#include "modules/widgets/calendar_widget.h"

namespace mm {

// Callback for receiving parsed events
typedef void (*ICSEventCallback)(const CalendarEvent& event, void* userData);

class ICSStreamParser
{
public:
    ICSStreamParser();
    ~ICSStreamParser();

    // Set the calendar color for all events from this calendar
    void SetCalendarColor(const char* color);

    // Set the time window for filtering events (only keep events starting within this window)
    void SetTimeWindow(unsigned startTime, unsigned endTime);

    // Set timezone offset for converting UTC times to local
    void SetTimezoneOffset(int offsetSeconds) { m_timezoneOffset = offsetSeconds; }

    // Feed data chunks to the parser
    // Call multiple times as data arrives
    void FeedData(const char* data, unsigned length);

    // Signal end of data - flushes any pending event
    void Finish();

    // Set callback for when events are parsed
    void SetEventCallback(ICSEventCallback callback, void* userData);

    // Get count of events parsed
    int GetEventCount() const { return m_eventCount; }

private:
    void ProcessBuffer();
    bool ParseEvent(const char* eventStart, const char* eventEnd, CalendarEvent* outEvent);
    unsigned ParseDateTime(const char* value, bool* isAllDay);
    bool FindProperty(const char* block, const char* blockEnd,
                      const char* propName, char* outValue, int maxLen);
    bool IsEventInWindow(unsigned eventStart, bool isAllDay);
    void ExpandRecurringEvent(const CalendarEvent& baseEvent, const struct RRule& rule,
                              const unsigned* exdates, int exdateCount);

    // Accumulation buffer for incomplete VEVENT blocks
    static const int BUFFER_SIZE = 16384;  // 16KB working buffer
    char m_buffer[BUFFER_SIZE];
    unsigned m_bufferLen;

    // Calendar metadata
    char m_calendarColor[16];

    // Time window for filtering
    unsigned m_windowStart;
    unsigned m_windowEnd;

    // Timezone offset (seconds, e.g. -21600 for Central)
    int m_timezoneOffset;

    // Callback
    ICSEventCallback m_callback;
    void* m_callbackUserData;

    // Stats
    int m_eventCount;
    int m_skippedCount;

    // RECURRENCE-ID tracking for duplicate prevention
    // Stores dates that have RECURRENCE-ID overrides (to skip when expanding RRULE)
    static const int MAX_RECURRENCE_IDS = 64;
    unsigned m_recurrenceIds[MAX_RECURRENCE_IDS];
    int m_recurrenceIdCount;
};

} // namespace mm

#endif // ICS_STREAM_PARSER_H
