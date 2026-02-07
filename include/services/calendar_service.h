#ifndef CALENDAR_SERVICE_H
#define CALENDAR_SERVICE_H

#include "services/http_client.h"
#include "services/ics_stream_parser.h"
#include "modules/widgets/calendar_widget.h"
#include "config/config.h"

namespace mm {

class CalendarService
{
public:
    CalendarService(HttpClient* pHttpClient);
    ~CalendarService();

    // Set time window for filtering events (default: now to 3 months from now)
    void SetTimeWindow(unsigned startTime, unsigned endTime);

    // Fetch and parse a single calendar, adding events to the provided array
    // Returns number of events added (total count after adding)
    int FetchCalendar(const CalendarConfig& calConfig,
                      CalendarEvent* events, int maxEvents, int currentCount);

private:
    HttpClient* m_pHttpClient;
    unsigned m_windowStart;
    unsigned m_windowEnd;
};

} // namespace mm

#endif // CALENDAR_SERVICE_H
