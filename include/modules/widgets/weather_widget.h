#ifndef WEATHER_WIDGET_H
#define WEATHER_WIDGET_H

#include "modules/widgets/widget_base.h"

namespace mm {

// Weather data structure
struct WeatherData {
    float temperature;      // Current temperature (with decimal)
    float feelsLike;        // "Feels like" temperature
    int humidity;           // Humidity percentage
    int windSpeed;          // Wind speed
    int windDirection;      // Wind direction in degrees (0-360)
    char condition[32];     // Weather condition text (e.g., "Cloudy")
    char city[48];          // City name (e.g., "KANSAS CITY")
    char state[16];         // State/region code (e.g., "US-MO")
    char sunriseTime[16];   // Sunrise time "6:45am"
    char sunsetTime[16];    // Sunset time "5:11pm"
    int weatherCode;        // WMO weather code for icon selection
    bool isMetric;          // true = Celsius, false = Fahrenheit
};

// Daily forecast data
struct ForecastDay {
    char dayName[12];       // "Mon", "Tue", etc.
    int high;
    int low;
    int weatherCode;        // WMO weather code for icon
    char condition[32];
};

class WeatherWidget : public WidgetBase
{
public:
    WeatherWidget(lv_obj_t* parent, CTimer* timer);
    ~WeatherWidget() override;

    bool Initialize() override;
    void Update() override;

    // Set weather data to display
    void SetWeatherData(const WeatherData& data);
    void SetForecast(const ForecastDay* days, int count);

    // Set timezone for day name calculation (e.g., "America/Chicago")
    void SetTimezone(const char* tzName);

private:
    void CreateUI();
    void UpdateDisplay();

    // Helper to convert wind degrees to cardinal direction
    const char* WindDirectionToCardinal(int degrees);

    // Helper to get day name for forecast (0=today, 1=tomorrow, etc.)
    const char* GetDayName(int daysFromToday) const;

    // LVGL objects - current weather
    lv_obj_t*   m_pLocationLabel;     // City, State
    lv_obj_t*   m_pWindIcon;          // Wind level icon
    lv_obj_t*   m_pWindLabel;         // Wind speed + direction
    lv_obj_t*   m_pSunsetIcon;        // Sunset icon
    lv_obj_t*   m_pSunsetLabel;       // Sunset time
    lv_obj_t*   m_pWeatherIcon;       // Weather condition icon
    lv_obj_t*   m_pTempLabel;         // Main temperature
    lv_obj_t*   m_pFeelsLikeLabel;    // Feels like text

    // LVGL objects - forecast (up to 5 days)
    static const int MAX_FORECAST_DAYS = 5;
    lv_obj_t*   m_pForecastContainer;
    lv_obj_t*   m_pForecastDays[MAX_FORECAST_DAYS];
    lv_obj_t*   m_pForecastIcons[MAX_FORECAST_DAYS];
    lv_obj_t*   m_pForecastTemps[MAX_FORECAST_DAYS];

    // State
    WeatherData m_weatherData;
    ForecastDay m_forecast[MAX_FORECAST_DAYS];
    int         m_forecastCount;
    char        m_timezone[64];     // Timezone name (e.g., "America/Chicago")

    // Buffers
    char        m_locationBuffer[72];   // "CITY, STATE"
    char        m_windSunBuffer[48];    // "=> 10 SSW  -O- 5:11 pm"
    char        m_tempBuffer[16];       // "61.5°"
    char        m_feelsLikeBuffer[32];  // "Feels like 49.1°"
};

} // namespace mm

#endif // WEATHER_WIDGET_H
