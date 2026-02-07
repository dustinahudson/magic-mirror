#include "services/calendar_service.h"
#include <circle/logger.h>
#include <circle/util.h>
#include <string.h>

namespace mm {

static const char FromCalSvc[] = "calsvc";

// Callback context for collecting events
struct EventCollector {
    CalendarEvent* events;
    int maxEvents;
    int count;
};

static void CollectEvent(const CalendarEvent& event, void* userData)
{
    EventCollector* collector = (EventCollector*)userData;
    if (collector->count < collector->maxEvents) {
        collector->events[collector->count++] = event;
    }
}

CalendarService::CalendarService(HttpClient* pHttpClient)
    : m_pHttpClient(pHttpClient),
      m_windowStart(0),
      m_windowEnd(0)
{
}

CalendarService::~CalendarService()
{
}

void CalendarService::SetTimeWindow(unsigned startTime, unsigned endTime)
{
    m_windowStart = startTime;
    m_windowEnd = endTime;
}

int CalendarService::FetchCalendar(const CalendarConfig& calConfig,
                                    CalendarEvent* events, int maxEvents, int currentCount)
{
    CLogger::Get()->Write(FromCalSvc, LogNotice, "Fetching calendar: %s", calConfig.name);

    // Use static to avoid large stack allocation
    static HttpResponse response;
    if (!m_pHttpClient->Get(calConfig.url, &response)) {
        CLogger::Get()->Write(FromCalSvc, LogWarning, "Failed to fetch calendar: %s", calConfig.name);
        return currentCount;
    }

    if (!response.success || response.statusCode != 200) {
        CLogger::Get()->Write(FromCalSvc, LogWarning, "Calendar fetch failed: HTTP %d", response.statusCode);
        return currentCount;
    }

    CLogger::Get()->Write(FromCalSvc, LogNotice, "Received %u bytes from %s",
                          response.bodyLength, calConfig.name);

    // Set up streaming parser
    ICSStreamParser parser;
    parser.SetCalendarColor(calConfig.color);
    parser.SetTimeWindow(m_windowStart, m_windowEnd);

    // Set up event collector
    EventCollector collector;
    collector.events = events;
    collector.maxEvents = maxEvents;
    collector.count = currentCount;

    parser.SetEventCallback(CollectEvent, &collector);

    // Feed all data to the parser (in chunks to simulate streaming)
    // This allows the parser to process and discard past events incrementally
    const unsigned CHUNK_SIZE = 8192;
    unsigned offset = 0;

    while (offset < response.bodyLength) {
        unsigned chunkLen = response.bodyLength - offset;
        if (chunkLen > CHUNK_SIZE) {
            chunkLen = CHUNK_SIZE;
        }
        parser.FeedData(response.body + offset, chunkLen);
        offset += chunkLen;
    }

    parser.Finish();

    CLogger::Get()->Write(FromCalSvc, LogNotice, "Calendar %s: kept %d events",
                          calConfig.name, collector.count - currentCount);

    return collector.count;
}

} // namespace mm
