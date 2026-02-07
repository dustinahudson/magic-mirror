#ifndef WEATHER_SERVICE_H
#define WEATHER_SERVICE_H

#include "services/http_client.h"
#include "modules/widgets/weather_widget.h"

namespace mm {

class WeatherService
{
public:
    WeatherService(HttpClient* pHttpClient);
    ~WeatherService();

    // Fetch current weather for a location (latitude, longitude)
    bool FetchWeather(float latitude, float longitude, WeatherData* outData);

    // Fetch 5-day forecast
    bool FetchForecast(float latitude, float longitude, ForecastDay* outForecast, int* outCount);

    // Set units (true = metric/Celsius, false = imperial/Fahrenheit)
    void SetMetric(bool metric) { m_isMetric = metric; }

    // Set location info (for display - Open-Meteo doesn't provide reverse geocoding)
    void SetLocationName(const char* city, const char* state);

    // Get configured location name
    const char* GetCity() const { return m_city; }
    const char* GetState() const { return m_state; }

private:
    // Parse JSON response for current weather
    bool ParseCurrentWeather(const char* json, WeatherData* outData);

    // Parse JSON response for forecast
    bool ParseForecast(const char* json, ForecastDay* outForecast, int* outCount);

    // Simple JSON value extraction helpers
    bool ExtractFloat(const char* json, const char* key, float* outValue);
    bool ExtractInt(const char* json, const char* key, int* outValue);
    bool ExtractString(const char* json, const char* key, char* outValue, size_t maxLen);

    HttpClient* m_pHttpClient;
    bool        m_isMetric;
    char        m_city[48];
    char        m_state[16];
};

} // namespace mm

#endif // WEATHER_SERVICE_H
