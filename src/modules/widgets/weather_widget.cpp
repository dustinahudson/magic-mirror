#include "modules/widgets/weather_widget.h"
#include "config/config.h"
#include "weather_icons.h"
#include <circle/util.h>
#include <circle/string.h>
#include <stdio.h>
#include <string.h>

namespace mm {

WeatherWidget::WeatherWidget(lv_obj_t* parent, CTimer* timer)
    : WidgetBase("Weather", parent, timer),
      m_pLocationLabel(nullptr),
      m_pWindIcon(nullptr),
      m_pWindLabel(nullptr),
      m_pSunsetIcon(nullptr),
      m_pSunsetLabel(nullptr),
      m_pWeatherIcon(nullptr),
      m_pTempLabel(nullptr),
      m_pFeelsLikeLabel(nullptr),
      m_pForecastContainer(nullptr),
      m_forecastCount(0)
{
    strcpy(m_timezone, "UTC");  // Default
    memset(&m_weatherData, 0, sizeof(m_weatherData));
    memset(m_forecast, 0, sizeof(m_forecast));
    memset(m_pForecastDays, 0, sizeof(m_pForecastDays));
    memset(m_pForecastIcons, 0, sizeof(m_pForecastIcons));
    memset(m_pForecastTemps, 0, sizeof(m_pForecastTemps));
    memset(m_locationBuffer, 0, sizeof(m_locationBuffer));
    memset(m_windSunBuffer, 0, sizeof(m_windSunBuffer));
    memset(m_tempBuffer, 0, sizeof(m_tempBuffer));
    memset(m_feelsLikeBuffer, 0, sizeof(m_feelsLikeBuffer));

    // Set default data
    m_weatherData.temperature = 72.0f;
    m_weatherData.feelsLike = 70.0f;
    m_weatherData.humidity = 45;
    m_weatherData.windSpeed = 5;
    m_weatherData.windDirection = 180;
    strcpy(m_weatherData.condition, "Partly Cloudy");
    strcpy(m_weatherData.city, "Dallas");
    strcpy(m_weatherData.state, "US-TX");
    strcpy(m_weatherData.sunriseTime, "6:45am");
    strcpy(m_weatherData.sunsetTime, "5:30pm");
    m_weatherData.isMetric = false;
}

WeatherWidget::~WeatherWidget()
{
    // LVGL objects are children of m_pContainer and will be deleted with it
}

bool WeatherWidget::Initialize()
{
    CreateUI();
    UpdateDisplay();
    return true;
}

void WeatherWidget::SetTimezone(const char* tzName)
{
    strncpy(m_timezone, tzName, sizeof(m_timezone) - 1);
    m_timezone[sizeof(m_timezone) - 1] = '\0';
}

const char* WeatherWidget::GetDayName(int daysFromToday) const
{
    static const char* dayNames[] = {"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"};
    static const char* specialNames[] = {"Today", "Tomorrow"};

    if (daysFromToday < 2) {
        return specialNames[daysFromToday];
    }

    // Get current time from timer and calculate day of week
    unsigned unixTime = m_pTimer ? m_pTimer->GetTime() : 0;
    if (unixTime == 0) {
        // Fallback if no valid time
        return dayNames[(daysFromToday + 1) % 7];  // Approximate
    }

    // Get timezone offset (handles DST)
    int offset = GetTimezoneOffset(m_timezone, unixTime);
    int localTime = (int)unixTime + offset;
    if (localTime < 0) localTime = 0;

    // Unix time starts Thu Jan 1 1970 (day 4)
    // Days since epoch (in local time)
    unsigned daysSinceEpoch = (unsigned)localTime / 86400;
    int currentDayOfWeek = (daysSinceEpoch + 4) % 7;  // 0=Sun, 1=Mon, etc.
    int targetDayOfWeek = (currentDayOfWeek + daysFromToday) % 7;

    return dayNames[targetDayOfWeek];
}

const char* WeatherWidget::WindDirectionToCardinal(int degrees)
{
    // Normalize to 0-360
    while (degrees < 0) degrees += 360;
    degrees = degrees % 360;

    // 16-point compass
    if (degrees >= 349 || degrees < 11) return "N";
    if (degrees < 34) return "NNE";
    if (degrees < 56) return "NE";
    if (degrees < 79) return "ENE";
    if (degrees < 101) return "E";
    if (degrees < 124) return "ESE";
    if (degrees < 146) return "SE";
    if (degrees < 169) return "SSE";
    if (degrees < 191) return "S";
    if (degrees < 214) return "SSW";
    if (degrees < 236) return "SW";
    if (degrees < 259) return "WSW";
    if (degrees < 281) return "W";
    if (degrees < 304) return "WNW";
    if (degrees < 326) return "NW";
    return "NNW";
}

void WeatherWidget::CreateUI()
{
    // Set container to use flex column layout for proper stacking
    lv_obj_set_flex_flow(m_pContainer, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_row(m_pContainer, 2, LV_PART_MAIN);

    // Location label (small, uppercase) - e.g., "KANSAS CITY, US-MO"
    m_pLocationLabel = lv_label_create(m_pContainer);
    lv_obj_set_style_text_color(m_pLocationLabel, lv_color_make(100, 100, 100), LV_PART_MAIN);
    lv_obj_set_style_text_font(m_pLocationLabel, &lv_font_montserrat_14, LV_PART_MAIN);
    lv_obj_set_style_border_side(m_pLocationLabel, LV_BORDER_SIDE_BOTTOM, LV_PART_MAIN);
    lv_obj_set_style_border_width(m_pLocationLabel, 1, LV_PART_MAIN);
    lv_obj_set_style_border_color(m_pLocationLabel, lv_color_make(60, 60, 60), LV_PART_MAIN);
    lv_obj_set_style_pad_bottom(m_pLocationLabel, 8, LV_PART_MAIN);
    lv_obj_set_width(m_pLocationLabel, lv_pct(100));

    // Wind and sunset info row with icons
    lv_obj_t* windSunRow = lv_obj_create(m_pContainer);
    lv_obj_set_style_bg_opa(windSunRow, LV_OPA_TRANSP, LV_PART_MAIN);
    lv_obj_set_style_border_width(windSunRow, 0, LV_PART_MAIN);
    lv_obj_set_style_pad_all(windSunRow, 0, LV_PART_MAIN);
    lv_obj_set_size(windSunRow, lv_pct(100), LV_SIZE_CONTENT);
    lv_obj_set_flex_flow(windSunRow, LV_FLEX_FLOW_ROW);
    lv_obj_set_style_pad_column(windSunRow, 6, LV_PART_MAIN);
    lv_obj_set_flex_align(windSunRow, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);

    // Wind icon (Beaufort scale)
    m_pWindIcon = lv_image_create(windSunRow);
    lv_image_set_src(m_pWindIcon, &icon_wind_3);  // Default icon

    // Wind label (speed + direction)
    m_pWindLabel = lv_label_create(windSunRow);
    lv_obj_set_style_text_color(m_pWindLabel, lv_color_white(), LV_PART_MAIN);
    lv_obj_set_style_text_font(m_pWindLabel, &lv_font_montserrat_24, LV_PART_MAIN);

    // Spacer
    lv_obj_t* spacer = lv_obj_create(windSunRow);
    lv_obj_set_style_bg_opa(spacer, LV_OPA_TRANSP, LV_PART_MAIN);
    lv_obj_set_style_border_width(spacer, 0, LV_PART_MAIN);
    lv_obj_set_size(spacer, 10, 1);

    // Sunset icon
    m_pSunsetIcon = lv_image_create(windSunRow);
    lv_image_set_src(m_pSunsetIcon, &icon_sunset);

    // Sunset label
    m_pSunsetLabel = lv_label_create(windSunRow);
    lv_obj_set_style_text_color(m_pSunsetLabel, lv_color_white(), LV_PART_MAIN);
    lv_obj_set_style_text_font(m_pSunsetLabel, &lv_font_montserrat_24, LV_PART_MAIN);

    // Create a row container for icon + temperature
    lv_obj_t* tempRow = lv_obj_create(m_pContainer);
    lv_obj_set_style_bg_opa(tempRow, LV_OPA_TRANSP, LV_PART_MAIN);
    lv_obj_set_style_border_width(tempRow, 0, LV_PART_MAIN);
    lv_obj_set_style_pad_all(tempRow, 0, LV_PART_MAIN);
    lv_obj_set_size(tempRow, lv_pct(100), LV_SIZE_CONTENT);
    lv_obj_set_flex_flow(tempRow, LV_FLEX_FLOW_ROW);
    lv_obj_set_style_pad_column(tempRow, 10, LV_PART_MAIN);
    lv_obj_set_flex_align(tempRow, LV_FLEX_ALIGN_START, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);

    // Weather icon (using image)
    m_pWeatherIcon = lv_image_create(tempRow);
    lv_image_set_src(m_pWeatherIcon, &weather_icon_clear_day);  // Default icon

    // Temperature (large) - e.g., "61.5°"
    m_pTempLabel = lv_label_create(tempRow);
    lv_obj_set_style_text_color(m_pTempLabel, lv_color_white(), LV_PART_MAIN);
    lv_obj_set_style_text_font(m_pTempLabel, &lv_font_montserrat_48, LV_PART_MAIN);

    // Feels like - e.g., "Feels like 49.1°"
    m_pFeelsLikeLabel = lv_label_create(m_pContainer);
    lv_obj_set_style_text_color(m_pFeelsLikeLabel, lv_color_make(100, 100, 100), LV_PART_MAIN);
    lv_obj_set_style_text_font(m_pFeelsLikeLabel, &lv_font_montserrat_18, LV_PART_MAIN);

    // Forecast section header with bottom border
    lv_obj_t* forecastHeader = lv_label_create(m_pContainer);
    lv_obj_set_style_text_color(forecastHeader, lv_color_make(100, 100, 100), LV_PART_MAIN);
    lv_obj_set_style_text_font(forecastHeader, &lv_font_montserrat_14, LV_PART_MAIN);
    lv_obj_set_style_pad_top(forecastHeader, 20, LV_PART_MAIN);
    lv_obj_set_style_border_side(forecastHeader, LV_BORDER_SIDE_BOTTOM, LV_PART_MAIN);
    lv_obj_set_style_border_width(forecastHeader, 1, LV_PART_MAIN);
    lv_obj_set_style_border_color(forecastHeader, lv_color_make(60, 60, 60), LV_PART_MAIN);
    lv_obj_set_style_pad_bottom(forecastHeader, 8, LV_PART_MAIN);
    lv_obj_set_width(forecastHeader, lv_pct(100));
    lv_label_set_text(forecastHeader, "WEATHER FORECAST");

    // Forecast container - uses flex column for day rows
    m_pForecastContainer = lv_obj_create(m_pContainer);
    lv_obj_set_style_bg_opa(m_pForecastContainer, LV_OPA_TRANSP, LV_PART_MAIN);
    lv_obj_set_style_border_width(m_pForecastContainer, 0, LV_PART_MAIN);
    lv_obj_set_style_pad_all(m_pForecastContainer, 0, LV_PART_MAIN);
    lv_obj_clear_flag(m_pForecastContainer, LV_OBJ_FLAG_SCROLLABLE);
    lv_obj_set_size(m_pForecastContainer, lv_pct(100), LV_SIZE_CONTENT);
    lv_obj_set_flex_flow(m_pForecastContainer, LV_FLEX_FLOW_COLUMN);
    lv_obj_set_style_pad_row(m_pForecastContainer, 2, LV_PART_MAIN);

    // Create forecast day rows
    for (int i = 0; i < MAX_FORECAST_DAYS; i++) {
        // Row container for each day
        lv_obj_t* dayRow = lv_obj_create(m_pForecastContainer);
        lv_obj_set_style_bg_opa(dayRow, LV_OPA_TRANSP, LV_PART_MAIN);
        lv_obj_set_style_border_width(dayRow, 0, LV_PART_MAIN);
        lv_obj_set_style_pad_all(dayRow, 0, LV_PART_MAIN);
        lv_obj_set_size(dayRow, lv_pct(100), LV_SIZE_CONTENT);
        lv_obj_set_flex_flow(dayRow, LV_FLEX_FLOW_ROW);
        lv_obj_set_flex_align(dayRow, LV_FLEX_ALIGN_SPACE_BETWEEN, LV_FLEX_ALIGN_CENTER, LV_FLEX_ALIGN_CENTER);

        // Day name (left side)
        m_pForecastDays[i] = lv_label_create(dayRow);
        lv_obj_set_style_text_color(m_pForecastDays[i], lv_color_make(150, 150, 150), LV_PART_MAIN);
        lv_obj_set_style_text_font(m_pForecastDays[i], &lv_font_montserrat_18, LV_PART_MAIN);
        lv_obj_set_width(m_pForecastDays[i], 100);  // Fixed width for alignment

        // Weather icon (center)
        m_pForecastIcons[i] = lv_image_create(dayRow);
        lv_image_set_src(m_pForecastIcons[i], &weather_icon_clear_day_small);

        // Temps (right side) - high/low
        m_pForecastTemps[i] = lv_label_create(dayRow);
        lv_obj_set_style_text_color(m_pForecastTemps[i], lv_color_make(180, 180, 180), LV_PART_MAIN);
        lv_obj_set_style_text_font(m_pForecastTemps[i], &lv_font_montserrat_18, LV_PART_MAIN);
    }
}

void WeatherWidget::Update()
{
    // Weather widget doesn't need per-frame updates
    // Data is pushed via SetWeatherData()
}

void WeatherWidget::SetWeatherData(const WeatherData& data)
{
    m_weatherData = data;
    UpdateDisplay();
}

void WeatherWidget::SetForecast(const ForecastDay* days, int count)
{
    m_forecastCount = count > MAX_FORECAST_DAYS ? MAX_FORECAST_DAYS : count;
    for (int i = 0; i < m_forecastCount; i++) {
        m_forecast[i] = days[i];
    }
    UpdateDisplay();
}

void WeatherWidget::UpdateDisplay()
{
    // Location (uppercase) - "CITY, STATE"
    snprintf(m_locationBuffer, sizeof(m_locationBuffer), "%s, %s",
             m_weatherData.city, m_weatherData.state);
    // Convert to uppercase
    for (char* p = m_locationBuffer; *p; p++) {
        if (*p >= 'a' && *p <= 'z') *p -= 32;
    }
    lv_label_set_text(m_pLocationLabel, m_locationBuffer);

    // Wind icon and label
    const lv_image_dsc_t* windIcon = get_wind_icon(m_weatherData.windSpeed);
    if (windIcon) {
        lv_image_set_src(m_pWindIcon, windIcon);
    }
    const char* windDir = WindDirectionToCardinal(m_weatherData.windDirection);
    snprintf(m_windSunBuffer, sizeof(m_windSunBuffer), "%d %s",
             m_weatherData.windSpeed, windDir);
    lv_label_set_text(m_pWindLabel, m_windSunBuffer);

    // Sunset label
    lv_label_set_text(m_pSunsetLabel, m_weatherData.sunsetTime);

    // Weather icon - use image based on weather code
    // TODO: Determine day/night based on current time vs sunrise/sunset
    bool isDay = true;  // For now, assume daytime
    const lv_image_dsc_t* weatherIcon = get_weather_icon(m_weatherData.weatherCode, isDay, false);
    if (weatherIcon) {
        lv_image_set_src(m_pWeatherIcon, weatherIcon);
    }

    // Temperature with decimal
    const char* degreeSymbol = "\xC2\xB0";  // UTF-8 degree symbol
    snprintf(m_tempBuffer, sizeof(m_tempBuffer), "%.1f%s",
             m_weatherData.temperature, degreeSymbol);
    lv_label_set_text(m_pTempLabel, m_tempBuffer);

    // Feels like
    snprintf(m_feelsLikeBuffer, sizeof(m_feelsLikeBuffer), "Feels like %.1f%s",
             m_weatherData.feelsLike, degreeSymbol);
    lv_label_set_text(m_pFeelsLikeLabel, m_feelsLikeBuffer);

    // Forecast - vertical list with correct day names
    for (int i = 0; i < MAX_FORECAST_DAYS; i++) {
        if (i < m_forecastCount) {
            // Get correct day name based on current date
            const char* dayName = GetDayName(i);
            lv_label_set_text(m_pForecastDays[i], dayName);

            // Forecast icon (small, always show day icons for forecast)
            const lv_image_dsc_t* forecastIcon = get_weather_icon(m_forecast[i].weatherCode, true, true);
            if (forecastIcon) {
                lv_image_set_src(m_pForecastIcons[i], forecastIcon);
            }

            // High/low temps
            char tempStr[24];
            snprintf(tempStr, sizeof(tempStr), "%.0f%s / %.0f%s",
                     (float)m_forecast[i].high, degreeSymbol,
                     (float)m_forecast[i].low, degreeSymbol);
            lv_label_set_text(m_pForecastTemps[i], tempStr);

            // Show the row
            lv_obj_t* parent = lv_obj_get_parent(m_pForecastDays[i]);
            lv_obj_clear_flag(parent, LV_OBJ_FLAG_HIDDEN);
        } else {
            // Hide unused forecast rows
            lv_obj_t* parent = lv_obj_get_parent(m_pForecastDays[i]);
            lv_obj_add_flag(parent, LV_OBJ_FLAG_HIDDEN);
        }
    }

    // Force LVGL to redraw the entire widget
    lv_obj_invalidate(m_pContainer);
}

} // namespace mm
