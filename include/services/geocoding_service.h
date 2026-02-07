#ifndef GEOCODING_SERVICE_H
#define GEOCODING_SERVICE_H

#include "services/http_client.h"

namespace mm {

// Location data returned from geocoding
struct GeoLocation {
    char city[48];
    char state[32];
    char stateAbbrev[8];    // e.g., "MO"
    char country[8];        // e.g., "US"
    float latitude;
    float longitude;
    bool valid;
};

class GeocodingService
{
public:
    GeocodingService(HttpClient* pHttpClient);
    ~GeocodingService();

    // Look up location by US zipcode
    // Uses zippopotam.us API (free, no key required)
    bool LookupZipcode(const char* zipcode, GeoLocation* outLocation);

private:
    // Parse JSON response from Open-Meteo geocoding API
    bool ParseResponse(const char* json, GeoLocation* outLocation);

    // Simple JSON value extraction
    bool ExtractString(const char* json, const char* key, char* outValue, size_t maxLen);
    bool ExtractFloat(const char* json, const char* key, float* outValue);

    HttpClient* m_pHttpClient;
};

} // namespace mm

#endif // GEOCODING_SERVICE_H
