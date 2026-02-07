#include "services/weather_service.h"
#include <circle/logger.h>
#include <circle/string.h>
#include <string.h>
#include <stdlib.h>
#include <stdio.h>

static const char FromWeather[] = "weather";

// Open-Meteo API base URL
static const char* WEATHER_HOST = "api.open-meteo.com";

namespace mm {

WeatherService::WeatherService(HttpClient* pHttpClient)
    : m_pHttpClient(pHttpClient),
      m_isMetric(false)
{
    m_city[0] = '\0';
    m_state[0] = '\0';
}

void WeatherService::SetLocationName(const char* city, const char* state)
{
    if (city) {
        strncpy(m_city, city, sizeof(m_city) - 1);
        m_city[sizeof(m_city) - 1] = '\0';
    }
    if (state) {
        strncpy(m_state, state, sizeof(m_state) - 1);
        m_state[sizeof(m_state) - 1] = '\0';
    }
}

WeatherService::~WeatherService()
{
}

bool WeatherService::FetchWeather(float latitude, float longitude, WeatherData* outData)
{
    if (!m_pHttpClient || !outData) {
        return false;
    }

    // Build API URL
    // Open-Meteo API: https://api.open-meteo.com/v1/forecast?latitude=32.78&longitude=-96.80&current_weather=true
    CString path;
    const char* tempUnit = m_isMetric ? "celsius" : "fahrenheit";
    const char* windUnit = m_isMetric ? "kmh" : "mph";

    path.Format("/v1/forecast?latitude=%.4f&longitude=%.4f&current=temperature_2m,relative_humidity_2m,apparent_temperature,weather_code,wind_speed_10m,wind_direction_10m&daily=sunrise,sunset&temperature_unit=%s&wind_speed_unit=%s&timezone=auto&forecast_days=1",
                latitude, longitude, tempUnit, windUnit);

    CLogger::Get()->Write(FromWeather, LogDebug, "Fetching weather from %s%s", WEATHER_HOST, (const char*)path);

    HttpResponse response;
    if (!m_pHttpClient->Get(WEATHER_HOST, (const char*)path, true, &response)) {
        CLogger::Get()->Write(FromWeather, LogError, "Failed to fetch weather data");
        return false;
    }

    CLogger::Get()->Write(FromWeather, LogDebug, "Got response: %u bytes", response.bodyLength);

    bool success = ParseCurrentWeather(response.body, outData);

    // Populate location name from service config
    if (success) {
        strncpy(outData->city, m_city, sizeof(outData->city) - 1);
        outData->city[sizeof(outData->city) - 1] = '\0';
        strncpy(outData->state, m_state, sizeof(outData->state) - 1);
        outData->state[sizeof(outData->state) - 1] = '\0';
    }

    return success;
}

bool WeatherService::FetchForecast(float latitude, float longitude, ForecastDay* outForecast, int* outCount)
{
    if (!m_pHttpClient || !outForecast || !outCount) {
        return false;
    }

    // Build API URL for daily forecast
    CString path;
    const char* tempUnit = m_isMetric ? "celsius" : "fahrenheit";

    path.Format("/v1/forecast?latitude=%.4f&longitude=%.4f&daily=temperature_2m_max,temperature_2m_min,weather_code&temperature_unit=%s&timezone=auto&forecast_days=5",
                latitude, longitude, tempUnit);

    CLogger::Get()->Write(FromWeather, LogDebug, "Fetching forecast from %s%s", WEATHER_HOST, (const char*)path);

    HttpResponse response;
    if (!m_pHttpClient->Get(WEATHER_HOST, (const char*)path, true, &response)) {
        CLogger::Get()->Write(FromWeather, LogError, "Failed to fetch forecast data");
        return false;
    }

    return ParseForecast(response.body, outForecast, outCount);
}

bool WeatherService::ParseCurrentWeather(const char* json, WeatherData* outData)
{
    // Initialize output
    memset(outData, 0, sizeof(WeatherData));
    outData->isMetric = m_isMetric;

    // Look for "current" object
    const char* current = strstr(json, "\"current\"");
    if (!current) {
        CLogger::Get()->Write(FromWeather, LogError, "No 'current' field in response");
        return false;
    }

    // Extract temperature (keep as float for decimal display)
    ExtractFloat(current, "\"temperature_2m\"", &outData->temperature);

    // Extract feels like (keep as float)
    ExtractFloat(current, "\"apparent_temperature\"", &outData->feelsLike);

    // Extract humidity
    float humidity;
    if (ExtractFloat(current, "\"relative_humidity_2m\"", &humidity)) {
        outData->humidity = (int)humidity;
    }

    // Extract wind speed
    float windSpeed;
    if (ExtractFloat(current, "\"wind_speed_10m\"", &windSpeed)) {
        outData->windSpeed = (int)(windSpeed + 0.5f);
    }

    // Extract wind direction
    float windDir;
    if (ExtractFloat(current, "\"wind_direction_10m\"", &windDir)) {
        outData->windDirection = (int)windDir;
    }

    // Extract weather code and convert to condition text
    int weatherCode;
    if (ExtractInt(current, "\"weather_code\"", &weatherCode)) {
        outData->weatherCode = weatherCode;

        // WMO weather codes: https://open-meteo.com/en/docs
        const char* condition = "Unknown";
        if (weatherCode == 0) condition = "Clear";
        else if (weatherCode <= 3) condition = "Partly Cloudy";
        else if (weatherCode <= 49) condition = "Foggy";
        else if (weatherCode <= 59) condition = "Drizzle";
        else if (weatherCode <= 69) condition = "Rain";
        else if (weatherCode <= 79) condition = "Snow";
        else if (weatherCode <= 84) condition = "Showers";
        else if (weatherCode <= 94) condition = "Snow Showers";
        else condition = "Thunderstorm";

        strncpy(outData->condition, condition, sizeof(outData->condition) - 1);
    }

    // Look for "daily" object to get sunrise/sunset
    // Open-Meteo returns these as arrays: "sunrise":["2024-01-07T06:45"]
    const char* daily = strstr(json, "\"daily\"");
    if (daily) {
        // Extract sunrise - find first value in array
        const char* sunriseKey = strstr(daily, "\"sunrise\"");
        if (sunriseKey) {
            const char* arrayStart = strchr(sunriseKey, '[');
            if (arrayStart) {
                const char* valueStart = strchr(arrayStart, '"');
                if (valueStart) {
                    valueStart++; // Skip opening quote
                    // Find the 'T' separator in the ISO datetime
                    const char* timeStr = strchr(valueStart, 'T');
                    if (timeStr) {
                        timeStr++; // Skip 'T'
                        int hour = 0, minute = 0;
                        if (sscanf(timeStr, "%d:%d", &hour, &minute) == 2) {
                            // Bound check for compiler warning
                            if (hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59) {
                                const char* ampm = (hour >= 12) ? "pm" : "am";
                                if (hour > 12) hour -= 12;
                                if (hour == 0) hour = 12;
                                snprintf(outData->sunriseTime, sizeof(outData->sunriseTime),
                                         "%d:%02d%s", hour, minute, ampm);
                            }
                        }
                    }
                }
            }
        }

        // Extract sunset
        const char* sunsetKey = strstr(daily, "\"sunset\"");
        if (sunsetKey) {
            const char* arrayStart = strchr(sunsetKey, '[');
            if (arrayStart) {
                const char* valueStart = strchr(arrayStart, '"');
                if (valueStart) {
                    valueStart++;
                    const char* timeStr = strchr(valueStart, 'T');
                    if (timeStr) {
                        timeStr++;
                        int hour = 0, minute = 0;
                        if (sscanf(timeStr, "%d:%d", &hour, &minute) == 2) {
                            // Bound check for compiler warning
                            if (hour >= 0 && hour <= 23 && minute >= 0 && minute <= 59) {
                                const char* ampm = (hour >= 12) ? "pm" : "am";
                                if (hour > 12) hour -= 12;
                                if (hour == 0) hour = 12;
                                snprintf(outData->sunsetTime, sizeof(outData->sunsetTime),
                                         "%d:%02d%s", hour, minute, ampm);
                            }
                        }
                    }
                }
            }
        }
    }

    CLogger::Get()->Write(FromWeather, LogNotice, "Weather: %.1f%s, %s, wind %d from %d deg",
                          outData->temperature,
                          m_isMetric ? "C" : "F",
                          outData->condition,
                          outData->windSpeed,
                          outData->windDirection);

    return true;
}

bool WeatherService::ParseForecast(const char* json, ForecastDay* outForecast, int* outCount)
{
    *outCount = 0;

    // Look for "daily" object
    const char* daily = strstr(json, "\"daily\"");
    if (!daily) {
        CLogger::Get()->Write(FromWeather, LogError, "No 'daily' field in response");
        return false;
    }

    // Find temperature and weather code arrays
    const char* maxTemps = strstr(daily, "\"temperature_2m_max\"");
    const char* minTemps = strstr(daily, "\"temperature_2m_min\"");
    const char* weatherCodes = strstr(daily, "\"weather_code\"");
    const char* times = strstr(daily, "\"time\"");

    if (!maxTemps || !minTemps || !times) {
        CLogger::Get()->Write(FromWeather, LogError, "Missing forecast fields");
        return false;
    }

    // Parse up to 5 days
    // Simple array parsing - find '[' then extract comma-separated values
    const char* maxArray = strchr(maxTemps, '[');
    const char* minArray = strchr(minTemps, '[');
    const char* codeArray = weatherCodes ? strchr(weatherCodes, '[') : NULL;
    const char* timeArray = strchr(times, '[');

    if (!maxArray || !minArray || !timeArray) {
        return false;
    }

    maxArray++; // skip '['
    minArray++;
    if (codeArray) codeArray++;
    timeArray++;

    static const char* dayNames[] = {"Sun", "Mon", "Tue", "Wed", "Thu", "Fri", "Sat"};

    for (int i = 0; i < 5; i++) {
        // Skip whitespace and quotes
        while (*maxArray == ' ' || *maxArray == '\n' || *maxArray == '"') maxArray++;
        while (*minArray == ' ' || *minArray == '\n' || *minArray == '"') minArray++;
        if (codeArray) while (*codeArray == ' ' || *codeArray == '\n') codeArray++;
        while (*timeArray == ' ' || *timeArray == '\n' || *timeArray == '"') timeArray++;

        if (*maxArray == ']' || *minArray == ']' || *timeArray == ']') {
            break;
        }

        // Parse high temp
        outForecast[i].high = (int)(strtof(maxArray, NULL) + 0.5f);

        // Parse low temp
        outForecast[i].low = (int)(strtof(minArray, NULL) + 0.5f);

        // Parse weather code
        if (codeArray && *codeArray != ']') {
            outForecast[i].weatherCode = (int)strtof(codeArray, NULL);
        } else {
            outForecast[i].weatherCode = 0;  // Default to clear
        }

        // Parse date (YYYY-MM-DD) and convert to day name
        // For simplicity, just use day index for now
        strncpy(outForecast[i].dayName, dayNames[(i + 1) % 7], sizeof(outForecast[i].dayName) - 1);

        strcpy(outForecast[i].condition, "");

        // Move to next value
        const char* nextMax = strchr(maxArray, ',');
        const char* nextMin = strchr(minArray, ',');
        const char* nextCode = codeArray ? strchr(codeArray, ',') : NULL;
        const char* nextTime = strchr(timeArray, ',');

        if (!nextMax || !nextMin || !nextTime) {
            *outCount = i + 1;
            break;
        }

        maxArray = nextMax + 1;
        minArray = nextMin + 1;
        if (nextCode) codeArray = nextCode + 1;
        timeArray = nextTime + 1;
        *outCount = i + 1;
    }

    CLogger::Get()->Write(FromWeather, LogNotice, "Forecast: %d days", *outCount);

    return *outCount > 0;
}

bool WeatherService::ExtractFloat(const char* json, const char* key, float* outValue)
{
    const char* pos = strstr(json, key);
    if (!pos) return false;

    pos = strchr(pos, ':');
    if (!pos) return false;

    pos++; // skip ':'
    while (*pos == ' ') pos++;

    *outValue = strtof(pos, NULL);
    return true;
}

bool WeatherService::ExtractInt(const char* json, const char* key, int* outValue)
{
    float f;
    if (!ExtractFloat(json, key, &f)) return false;
    *outValue = (int)f;
    return true;
}

bool WeatherService::ExtractString(const char* json, const char* key, char* outValue, size_t maxLen)
{
    const char* pos = strstr(json, key);
    if (!pos) return false;

    pos = strchr(pos, ':');
    if (!pos) return false;

    pos = strchr(pos, '"');
    if (!pos) return false;

    pos++; // skip opening quote
    const char* end = strchr(pos, '"');
    if (!end) return false;

    size_t len = end - pos;
    if (len >= maxLen) len = maxLen - 1;

    strncpy(outValue, pos, len);
    outValue[len] = '\0';
    return true;
}

} // namespace mm
