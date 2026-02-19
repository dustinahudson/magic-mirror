// TLS support - MUST be included FIRST to avoid time_t conflicts
#include <circle-mbedtls/tlssimplesupport.h>

#include "core/kernel.h"
#include "core/application.h"
#include "modules/widgets/datetime_widget.h"
#include "modules/widgets/weather_widget.h"
#include "modules/widgets/calendar_widget.h"
#include "modules/widgets/upcoming_events_widget.h"
#include "services/http_client.h"
#include "services/weather_service.h"
#include "services/geocoding_service.h"
#include "services/calendar_service.h"
#include "services/update_service.h"
#include "config/config.h"
#include <circle/net/dnsclient.h>
#include <circle/net/ntpclient.h>
#include <circle/util.h>
#include <lvgl/lvgl/lvgl.h>
#include <circle/memory.h>
#include <stdio.h>
#include <string.h>

#define DRIVE           "SD:"
#define FIRMWARE_PATH   DRIVE "/firmware/"
#define CONFIG_FILE     DRIVE "/wpa_supplicant.conf"

static const char FromKernel[] = "kernel";

CKernel::CKernel()
    : m_Screen(m_Options.GetWidth(), m_Options.GetHeight()),
      m_Timer(&m_Interrupt),
      m_Logger(m_Options.GetLogLevel(), &m_Timer),
      m_USBHCI(&m_Interrupt, &m_Timer),
      m_LVGL(&m_Screen),
#ifdef USE_USB_SERIAL_GADGET
      m_USBSerial(&m_Interrupt),
#endif
      m_EMMC(&m_Interrupt, &m_Timer, &m_ActLED),
      m_WLAN(FIRMWARE_PATH),
      m_Net(0, 0, 0, 0, "magicmirror", NetDeviceTypeWLAN),
      m_WPASupplicant(CONFIG_FILE),
      m_pTLS(nullptr),
      m_bNetworkReady(FALSE),
      m_bRebootRequested(FALSE)
{
    m_ActLED.Blink(5);
}

CKernel::~CKernel()
{
    if (m_pTLS) {
        delete m_pTLS;
        m_pTLS = nullptr;
    }
}

boolean CKernel::Initialize()
{
    boolean bOK = TRUE;

    if (bOK) {
        bOK = m_Screen.Initialize();
    }

    if (bOK) {
        bOK = m_Serial.Initialize(115200);
    }

    if (bOK) {
        // Use serial for logging to avoid conflict with direct framebuffer access
        bOK = m_Logger.Initialize(&m_Serial);
    }

    if (bOK) {
        bOK = m_Interrupt.Initialize();
    }

    if (bOK) {
        bOK = m_Timer.Initialize();
    }

    // USB HCI must be initialized before EMMC for proper operation
    if (bOK) {
        bOK = m_USBHCI.Initialize();
    }

#ifdef USE_USB_SERIAL_GADGET
    if (bOK) {
        bOK = m_USBSerial.Initialize();
        if (bOK) {
            CDevice* pUSBSerialDevice = m_DeviceNameService.GetDevice("utty1", FALSE);
            if (pUSBSerialDevice) {
                m_Logger.Write(FromKernel, LogNotice, "USB serial available");
            }
        }
    }
#endif

    if (bOK) {
        bOK = m_EMMC.Initialize();
    }

    if (bOK) {
        if (f_mount(&m_FileSystem, DRIVE, 1) != FR_OK) {
            m_Logger.Write(FromKernel, LogError, "Cannot mount drive: %s", DRIVE);
            bOK = FALSE;
        }
    }

    // Start file logging now that filesystem is available
    if (bOK) {
        if (!m_FileLogger.Initialize()) {
            m_Logger.Write(FromKernel, LogWarning, "File logger init failed");
        }
    }

    // Initialize LVGL for display management
    if (bOK) {
        bOK = m_LVGL.Initialize();
        if (bOK) {
            m_Logger.Write(FromKernel, LogNotice, "LVGL initialized");
        } else {
            m_Logger.Write(FromKernel, LogError, "LVGL initialization failed");
        }
    }

    if (bOK) {
        bOK = m_WLAN.Initialize();
        if (!bOK) {
            m_Logger.Write(FromKernel, LogWarning, "WLAN initialization failed");
            // Continue without network
            bOK = TRUE;
        } else {
            m_bNetworkReady = TRUE;
        }
    }

    if (bOK && m_bNetworkReady) {
        bOK = m_Net.Initialize(FALSE);
        if (!bOK) {
            m_Logger.Write(FromKernel, LogWarning, "Network initialization failed");
            m_bNetworkReady = FALSE;
            bOK = TRUE;
        }
    }

    if (bOK && m_bNetworkReady) {
        bOK = m_WPASupplicant.Initialize();
        if (!bOK) {
            m_Logger.Write(FromKernel, LogWarning, "WPA supplicant initialization failed");
            m_bNetworkReady = FALSE;
            bOK = TRUE;
        }
    }

    // Create TLS support (after network is initialized)
    if (bOK && m_bNetworkReady) {
        m_pTLS = new CircleMbedTLS::CTLSSimpleSupport(&m_Net);
        m_Logger.Write(FromKernel, LogNotice, "TLS support initialized");
    }

    return bOK;
}

TShutdownMode CKernel::Run()
{
    m_Logger.Write(FromKernel, LogNotice, "Magic Mirror starting...");
    m_Logger.Write(FromKernel, LogNotice, "Compile time: " __DATE__ " " __TIME__);
#ifdef APP_VERSION
    m_Logger.Write(FromKernel, LogNotice, "Version: " APP_VERSION);
#endif

    // Clean up stale partial downloads from previous failed update
    f_unlink("SD:/kernel.new");

    if (m_bNetworkReady) {
        m_Logger.Write(FromKernel, LogNotice, "Waiting for network (60s timeout)...");

        // Wait for network with timeout - increased for DHCP reliability
        const unsigned NETWORK_TIMEOUT_MS = 60000;
        unsigned startTime = m_Timer.GetClockTicks() / 1000;
        unsigned lastLog = 0;

        while (!m_Net.IsRunning()) {
            unsigned elapsed = (m_Timer.GetClockTicks() / 1000) - startTime;

            // Log progress every 10 seconds
            if (elapsed / 10000 > lastLog) {
                lastLog = elapsed / 10000;
                m_Logger.Write(FromKernel, LogNotice, "Still waiting for DHCP... %us", elapsed / 1000);
            }

            if (elapsed > NETWORK_TIMEOUT_MS) {
                m_Logger.Write(FromKernel, LogWarning, "Network timeout - continuing without network");
                m_bNetworkReady = FALSE;
                break;
            }
            m_Scheduler.MsSleep(100);
        }

        if (m_bNetworkReady) {
            CString ipString;
            m_Net.GetConfig()->GetIPAddress()->Format(&ipString);
            m_Logger.Write(FromKernel, LogNotice, "Network ready, IP: %s",
                          (const char*)ipString);

            m_Logger.Write(FromKernel, LogNotice, "Syncing time via NTP...");

            CDNSClient dnsClient(&m_Net);
            CIPAddress ntpServer;
            if (dnsClient.Resolve("pool.ntp.org", &ntpServer)) {
                CNTPClient ntpClient(&m_Net);
                unsigned ntpTime = ntpClient.GetTime(ntpServer);
                if (ntpTime != 0) {
                    m_Timer.SetTime(ntpTime, FALSE);
                    m_Logger.Write(FromKernel, LogNotice, "Time synchronized");
                } else {
                    m_Logger.Write(FromKernel, LogWarning, "NTP sync failed");
                }
            } else {
                m_Logger.Write(FromKernel, LogWarning, "DNS resolution failed for NTP server");
            }
        }
    } else {
        m_Logger.Write(FromKernel, LogNotice, "Starting without network");
    }

    // Set up the main screen
    m_Logger.Write(FromKernel, LogNotice, "Creating UI...");

    lv_obj_t* scr = lv_screen_active();
    lv_obj_set_style_bg_color(scr, lv_color_black(), LV_PART_MAIN);
    lv_obj_set_style_bg_opa(scr, LV_OPA_COVER, LV_PART_MAIN);

    // Layout: Left column stacks content (25%), Calendar takes right 75%
    const int LEFT_COL_WIDTH = m_Screen.GetWidth() / 4;
    const int PADDING = 20;

    // Create left column container with flex layout for automatic stacking
    lv_obj_t* leftColumn = lv_obj_create(scr);
    lv_obj_set_size(leftColumn, LEFT_COL_WIDTH, m_Screen.GetHeight() - (2 * PADDING));
    lv_obj_set_pos(leftColumn, PADDING, PADDING);
    lv_obj_set_style_bg_opa(leftColumn, LV_OPA_TRANSP, LV_PART_MAIN);
    lv_obj_set_style_border_width(leftColumn, 0, LV_PART_MAIN);
    lv_obj_set_style_pad_all(leftColumn, 0, LV_PART_MAIN);
    lv_obj_set_style_pad_row(leftColumn, 25, LV_PART_MAIN);  // Gap between stacked widgets
    lv_obj_set_flex_flow(leftColumn, LV_FLEX_FLOW_COLUMN);
    lv_obj_clear_flag(leftColumn, LV_OBJ_FLAG_SCROLLABLE);

    // Create DateTime widget - stacks at top of left column
    mm::DateTimeWidget dateTimeWidget(leftColumn, &m_Timer);
    dateTimeWidget.SetContentSize();  // Size to content for flex stacking
    // Central time zone offset: UTC-6 hours = -21600 seconds
    dateTimeWidget.SetTimezoneOffset(-6 * 3600);
    dateTimeWidget.Initialize();

    m_Logger.Write(FromKernel, LogNotice, "DateTime widget created");

    // Create Weather widget - stacks below datetime
    mm::WeatherWidget weatherWidget(leftColumn, &m_Timer);
    weatherWidget.SetContentSize();  // Size to content for flex stacking
    weatherWidget.Initialize();

    // Load configuration
    mm::Config config;
    mm::Config::LoadFromFile("SD:/config/config.json", &config);
    m_Logger.Write(FromKernel, LogNotice, "Config loaded: zipcode=%s, units=%s",
                   config.weather.zipcode, config.weather.units);

    // Set timezone on weather widget from config
    weatherWidget.SetTimezone(config.timezone);

    // Fetch real weather data if network is available
    mm::HttpClient* pHttpClient = nullptr;
    mm::WeatherService* pWeatherService = nullptr;
    mm::GeocodingService* pGeoService = nullptr;
    mm::GeoLocation location = {0};

    if (m_bNetworkReady && m_pTLS) {
        m_Logger.Write(FromKernel, LogNotice, "Creating HTTP client...");
        pHttpClient = new mm::HttpClient(&m_Net, m_pTLS);

        // Geocode the zipcode from config
        pGeoService = new mm::GeocodingService(pHttpClient);
        if (pGeoService->LookupZipcode(config.weather.zipcode, &location)) {
            m_Logger.Write(FromKernel, LogNotice, "Location: %s, %s (%.4f, %.4f)",
                           location.city, location.stateAbbrev,
                           location.latitude, location.longitude);
        } else {
            m_Logger.Write(FromKernel, LogWarning, "Geocoding failed, using defaults");
            // Fallback to Kansas City
            strcpy(location.city, "Kansas City");
            strcpy(location.stateAbbrev, "MO");
            strcpy(location.country, "US");
            location.latitude = 39.0997f;
            location.longitude = -94.5786f;
            location.valid = true;
        }

        // Create weather service
        pWeatherService = new mm::WeatherService(pHttpClient);
        bool isMetric = (strcmp(config.weather.units, "metric") == 0);
        pWeatherService->SetMetric(isMetric);

        // Format location display: "City, US-XX"
        char stateDisplay[24];
        snprintf(stateDisplay, sizeof(stateDisplay), "%s-%s",
                 location.country, location.stateAbbrev);
        pWeatherService->SetLocationName(location.city, stateDisplay);

        // Fetch current weather
        mm::WeatherData weatherData;
        if (pWeatherService->FetchWeather(location.latitude, location.longitude, &weatherData)) {
            weatherWidget.SetWeatherData(weatherData);
            m_Logger.Write(FromKernel, LogNotice, "Weather updated: %.1f%s %s",
                          weatherData.temperature,
                          weatherData.isMetric ? "C" : "F",
                          weatherData.condition);
        } else {
            m_Logger.Write(FromKernel, LogWarning, "Failed to fetch weather data");
        }

        // Fetch forecast
        mm::ForecastDay forecast[5];
        int forecastCount = 0;
        if (pWeatherService->FetchForecast(location.latitude, location.longitude, forecast, &forecastCount)) {
            weatherWidget.SetForecast(forecast, forecastCount);
            m_Logger.Write(FromKernel, LogNotice, "Forecast updated: %d days", forecastCount);
        } else {
            m_Logger.Write(FromKernel, LogWarning, "Failed to fetch forecast data");
        }
    } else {
        m_Logger.Write(FromKernel, LogNotice, "Network not ready, using sample weather data");
        // Set sample forecast data as fallback
        // ForecastDay: dayName, high, low, weatherCode, condition
        mm::ForecastDay forecast[5] = {
            {"Mon", 75, 58, 0, "Sunny"},      // 0 = clear
            {"Tue", 72, 55, 3, "Cloudy"},     // 3 = overcast
            {"Wed", 68, 52, 61, "Rain"},      // 61 = rain
            {"Thu", 70, 54, 3, "Cloudy"},     // 3 = overcast
            {"Fri", 74, 56, 0, "Sunny"}       // 0 = clear
        };
        weatherWidget.SetForecast(forecast, 5);
    }

    m_Logger.Write(FromKernel, LogNotice, "Weather widget created");

    // Create Upcoming Events widget - stacks below weather forecast in left column
    mm::UpcomingEventsWidget upcomingEventsWidget(leftColumn, &m_Timer);
    upcomingEventsWidget.SetFillHeight();  // Fill remaining vertical space
    upcomingEventsWidget.SetTimezone(config.timezone);
    upcomingEventsWidget.SetMaxEvents(10);

    m_Logger.Write(FromKernel, LogNotice, "Upcoming Events widget created");

    // Create Calendar widget - right 2/3 of screen, full height
    // Position it absolutely to the right of the left column
    const int CAL_X = LEFT_COL_WIDTH + PADDING + 20;  // After left column + gap
    const int CAL_WIDTH = m_Screen.GetWidth() - CAL_X - PADDING;
    const int CAL_HEIGHT = m_Screen.GetHeight() - (2 * PADDING);

    mm::CalendarWidget calendarWidget(scr, &m_Timer);
    calendarWidget.SetAbsolutePosition(CAL_X, PADDING, CAL_WIDTH, CAL_HEIGHT);
    calendarWidget.SetTimezone(config.timezone);

    // Fetch calendar events BEFORE Initialize() so they render on first draw
    int icsEventCount = 0;
    static char calDebug[64] = "no fetch";
    if (m_bNetworkReady && pHttpClient && config.nCalendars > 0) {
        m_Logger.Write(FromKernel, LogNotice, "Fetching %d calendars...", config.nCalendars);

        mm::CalendarService calService(pHttpClient);

        // Set time window: now to 3 months from now
        unsigned now = m_Timer.GetTime();
        unsigned threeMonths = 90 * 24 * 60 * 60;  // ~90 days
        calService.SetTimeWindow(now, now + threeMonths);

        // Temporary array to collect all events
        static mm::CalendarEvent allEvents[200];
        int totalEvents = 0;

        for (int i = 0; i < config.nCalendars && totalEvents < 200; i++) {
            int before = totalEvents;
            totalEvents = calService.FetchCalendar(config.calendars[i],
                                                    allEvents, 200, totalEvents);
            m_Logger.Write(FromKernel, LogNotice, "Calendar %d: added %d events",
                          i, totalEvents - before);
        }

        m_Logger.Write(FromKernel, LogNotice, "Total calendar events: %d", totalEvents);
        icsEventCount = totalEvents;
        snprintf(calDebug, sizeof(calDebug), "Cal:%d", totalEvents);

        // Add events to calendar widget and upcoming events widget
        for (int i = 0; i < totalEvents; i++) {
            calendarWidget.AddEvent(allEvents[i]);
            upcomingEventsWidget.AddEvent(allEvents[i]);
        }
    }

    calendarWidget.Initialize();
    upcomingEventsWidget.Initialize();

    m_Logger.Write(FromKernel, LogNotice, "Calendar widget created");

    // Create a status label at bottom - show network and event count for debugging
    lv_obj_t* status = lv_label_create(scr);
    if (m_bNetworkReady) {
        CString ipString;
        m_Net.GetConfig()->GetIPAddress()->Format(&ipString);
        lv_label_set_text_fmt(status, "%s | Cals:%d Evt:%d | " APP_VERSION,
                              (const char*)ipString, config.nCalendars, icsEventCount);
    } else {
        lv_label_set_text_fmt(status, "Offline | Cals:%d | " APP_VERSION, config.nCalendars);
    }
    lv_obj_set_style_text_color(status, lv_color_make(80, 80, 80), LV_PART_MAIN);
    lv_obj_set_style_text_font(status, &lv_font_montserrat_14, LV_PART_MAIN);
    lv_obj_align(status, LV_ALIGN_BOTTOM_RIGHT, -20, -10);

    m_Logger.Write(FromKernel, LogNotice, "Entering main loop...");

    // Calendar refresh tracking
    unsigned lastCalendarRefresh = m_Timer.GetTime();
    const unsigned CALENDAR_REFRESH_INTERVAL = 5 * 60;  // 5 minutes

    // Weather refresh tracking
    unsigned lastWeatherRefresh = m_Timer.GetTime();
    const unsigned WEATHER_REFRESH_INTERVAL = 30 * 60;  // 30 minutes

    // Update check tracking
    unsigned lastUpdateCheck = 0;  // Check soon after boot
    const unsigned UPDATE_CHECK_INTERVAL = 60 * 60;  // 1 hour

    unsigned nLoopCount = 0;
    while (1) {
        // Update widgets
        dateTimeWidget.Update();
        weatherWidget.Update();
        calendarWidget.Update();
        upcomingEventsWidget.Update();

        // Refresh calendars every 5 minutes
        unsigned now = m_Timer.GetTime();

        // Update status with sync countdown every ~10 seconds
        if (nLoopCount % 1000 == 0) {
            unsigned secsSinceRefresh = now - lastCalendarRefresh;
            unsigned secsUntilRefresh = (secsSinceRefresh < CALENDAR_REFRESH_INTERVAL)
                                        ? (CALENDAR_REFRESH_INTERVAL - secsSinceRefresh) : 0;
            if (m_bNetworkReady) {
                CString ipString;
                m_Net.GetConfig()->GetIPAddress()->Format(&ipString);
                lv_label_set_text_fmt(status, "%s | Cals:%d Evt:%d | Sync:%us | " APP_VERSION,
                                      (const char*)ipString, config.nCalendars, icsEventCount, secsUntilRefresh);
            }
        }

        if (m_bNetworkReady && pHttpClient && config.nCalendars > 0 &&
            (now - lastCalendarRefresh) >= CALENDAR_REFRESH_INTERVAL) {

            m_Logger.Write(FromKernel, LogNotice, "Refreshing calendars...");

            mm::CalendarService calService(pHttpClient);
            unsigned threeMonths = 90 * 24 * 60 * 60;
            calService.SetTimeWindow(now, now + threeMonths);

            static mm::CalendarEvent refreshEvents[200];
            int totalEvents = 0;

            for (int i = 0; i < config.nCalendars && totalEvents < 200; i++) {
                totalEvents = calService.FetchCalendar(config.calendars[i],
                                                        refreshEvents, 200, totalEvents);
            }

            // Clear old events and add new ones to both widgets
            calendarWidget.ClearEvents();
            upcomingEventsWidget.ClearEvents();
            for (int i = 0; i < totalEvents; i++) {
                calendarWidget.AddEvent(refreshEvents[i]);
                upcomingEventsWidget.AddEvent(refreshEvents[i]);
            }
            calendarWidget.Refresh();  // Force re-render with new events
            upcomingEventsWidget.Refresh();

            icsEventCount = totalEvents;
            lastCalendarRefresh = now;

            m_Logger.Write(FromKernel, LogNotice, "Calendar refresh: %d events", totalEvents);

            // Update status display
            CString ipString;
            m_Net.GetConfig()->GetIPAddress()->Format(&ipString);
            lv_label_set_text_fmt(status, "%s | Cals:%d Evt:%d | " APP_VERSION,
                                  (const char*)ipString, config.nCalendars, icsEventCount);
        }

        // Weather refresh
        if (m_bNetworkReady && pWeatherService && location.valid &&
            (now - lastWeatherRefresh) >= WEATHER_REFRESH_INTERVAL) {

            m_Logger.Write(FromKernel, LogNotice, "Weather sync...");

            bool weatherUpdated = false;

            // Fetch current weather
            mm::WeatherData weatherData;
            if (pWeatherService->FetchWeather(location.latitude, location.longitude, &weatherData)) {
                weatherWidget.SetWeatherData(weatherData);
                weatherUpdated = true;
                m_Logger.Write(FromKernel, LogNotice, "Weather updated: %.1f%s %s",
                              weatherData.temperature,
                              weatherData.isMetric ? "C" : "F",
                              weatherData.condition);
            } else {
                m_Logger.Write(FromKernel, LogWarning, "Weather fetch failed");
            }

            // Fetch forecast
            mm::ForecastDay forecast[5];
            int forecastCount = 0;
            if (pWeatherService->FetchForecast(location.latitude, location.longitude, forecast, &forecastCount)) {
                weatherWidget.SetForecast(forecast, forecastCount);
                weatherUpdated = true;
                m_Logger.Write(FromKernel, LogNotice, "Forecast updated: %d days", forecastCount);
            }

            // Only advance timer on success so failed fetches are retried
            if (weatherUpdated) {
                lastWeatherRefresh = now;
            }
        }

        // Update check — every hour
        if (m_bNetworkReady && pHttpClient && config.update.enabled &&
            (now - lastUpdateCheck) >= UPDATE_CHECK_INTERVAL) {

            m_Logger.Write(FromKernel, LogNotice, "Checking for updates...");
            mm::UpdateService updateService(pHttpClient);
            if (updateService.CheckAndUpdate()) {
                m_Logger.Write(FromKernel, LogNotice, "Update installed, rebooting...");
                m_bRebootRequested = true;
                break;
            }
            lastUpdateCheck = now;
        }

        // Flush log events to file
        m_FileLogger.Update();

        // Update LVGL - this handles all rendering
        m_LVGL.Update(FALSE);

        // Log heartbeat every 60 seconds
        if (nLoopCount % 6000 == 0 && nLoopCount > 0) {
            size_t heapFree = CMemorySystem::Get()->GetHeapFreeSpace(HEAP_ANY);
            m_Logger.Write(FromKernel, LogNotice, "Running %u min | Heap free: %u KB",
                           nLoopCount / 6000, (unsigned)(heapFree / 1024));
        }

        nLoopCount++;
        m_Scheduler.MsSleep(10);
    }

    // Flush and close log file before shutdown
    m_FileLogger.Close();

    // Clean up
    delete pGeoService;
    delete pWeatherService;
    delete pHttpClient;

    return m_bRebootRequested ? ShutdownReboot : ShutdownHalt;
}
