#include "services/geocoding_service.h"
#include <circle/logger.h>
#include <circle/string.h>
#include <string.h>
#include <stdlib.h>

static const char FromGeo[] = "geocoding";

// Open-Meteo Geocoding API (HTTPS, free, no key required)
static const char* GEO_HOST = "geocoding-api.open-meteo.com";

namespace mm {

GeocodingService::GeocodingService(HttpClient* pHttpClient)
    : m_pHttpClient(pHttpClient)
{
}

GeocodingService::~GeocodingService()
{
}

bool GeocodingService::LookupZipcode(const char* zipcode, GeoLocation* outLocation)
{
    if (!m_pHttpClient || !outLocation || !zipcode) {
        return false;
    }

    // Initialize output
    memset(outLocation, 0, sizeof(GeoLocation));

    // Build API URL using Open-Meteo geocoding
    // Search with zipcode as name, limited to 1 result
    CString path;
    path.Format("/v1/search?name=%s&count=1&language=en&format=json", zipcode);

    CLogger::Get()->Write(FromGeo, LogDebug, "Looking up zipcode %s via Open-Meteo", zipcode);

    HttpResponse response;
    if (!m_pHttpClient->Get(GEO_HOST, (const char*)path, true, &response)) {
        CLogger::Get()->Write(FromGeo, LogError, "Failed to fetch geocoding data");
        return false;
    }

    CLogger::Get()->Write(FromGeo, LogDebug, "Got response: %u bytes", response.bodyLength);

    return ParseResponse(response.body, outLocation);
}

bool GeocodingService::ParseResponse(const char* json, GeoLocation* outLocation)
{
    // Open-Meteo geocoding response format:
    // {
    //   "results": [
    //     {
    //       "id": 4393217,
    //       "name": "Kansas City",
    //       "latitude": 39.09973,
    //       "longitude": -94.57857,
    //       "country_code": "US",
    //       "admin1": "Missouri",
    //       "timezone": "America/Chicago",
    //       ...
    //     }
    //   ]
    // }

    // Check for results array
    const char* results = strstr(json, "\"results\"");
    if (!results) {
        CLogger::Get()->Write(FromGeo, LogError, "No 'results' in geocoding response");
        return false;
    }

    // Find the first result object
    const char* resultStart = strchr(results, '{');
    if (!resultStart) {
        CLogger::Get()->Write(FromGeo, LogError, "No result object found");
        return false;
    }

    // Extract city name
    ExtractString(resultStart, "\"name\"", outLocation->city, sizeof(outLocation->city));

    // Extract country code
    ExtractString(resultStart, "\"country_code\"", outLocation->country, sizeof(outLocation->country));

    // Extract state (admin1 in Open-Meteo)
    ExtractString(resultStart, "\"admin1\"", outLocation->state, sizeof(outLocation->state));

    // Create state abbreviation from first 2 chars if it's a US state
    // For now, just use country code format: "US-MO" style
    if (outLocation->state[0] != '\0') {
        // Map common state names to abbreviations
        const struct { const char* name; const char* abbrev; } states[] = {
            {"Alabama", "AL"}, {"Alaska", "AK"}, {"Arizona", "AZ"}, {"Arkansas", "AR"},
            {"California", "CA"}, {"Colorado", "CO"}, {"Connecticut", "CT"}, {"Delaware", "DE"},
            {"Florida", "FL"}, {"Georgia", "GA"}, {"Hawaii", "HI"}, {"Idaho", "ID"},
            {"Illinois", "IL"}, {"Indiana", "IN"}, {"Iowa", "IA"}, {"Kansas", "KS"},
            {"Kentucky", "KY"}, {"Louisiana", "LA"}, {"Maine", "ME"}, {"Maryland", "MD"},
            {"Massachusetts", "MA"}, {"Michigan", "MI"}, {"Minnesota", "MN"}, {"Mississippi", "MS"},
            {"Missouri", "MO"}, {"Montana", "MT"}, {"Nebraska", "NE"}, {"Nevada", "NV"},
            {"New Hampshire", "NH"}, {"New Jersey", "NJ"}, {"New Mexico", "NM"}, {"New York", "NY"},
            {"North Carolina", "NC"}, {"North Dakota", "ND"}, {"Ohio", "OH"}, {"Oklahoma", "OK"},
            {"Oregon", "OR"}, {"Pennsylvania", "PA"}, {"Rhode Island", "RI"}, {"South Carolina", "SC"},
            {"South Dakota", "SD"}, {"Tennessee", "TN"}, {"Texas", "TX"}, {"Utah", "UT"},
            {"Vermont", "VT"}, {"Virginia", "VA"}, {"Washington", "WA"}, {"West Virginia", "WV"},
            {"Wisconsin", "WI"}, {"Wyoming", "WY"}, {"District of Columbia", "DC"},
            {nullptr, nullptr}
        };

        outLocation->stateAbbrev[0] = '\0';
        for (int i = 0; states[i].name != nullptr; i++) {
            if (strcmp(outLocation->state, states[i].name) == 0) {
                strncpy(outLocation->stateAbbrev, states[i].abbrev, sizeof(outLocation->stateAbbrev) - 1);
                break;
            }
        }
    }

    // Extract latitude
    ExtractFloat(resultStart, "\"latitude\"", &outLocation->latitude);

    // Extract longitude
    ExtractFloat(resultStart, "\"longitude\"", &outLocation->longitude);

    // Validate we got the essential data
    if (outLocation->city[0] != '\0' &&
        outLocation->latitude != 0.0f &&
        outLocation->longitude != 0.0f) {
        outLocation->valid = true;
        CLogger::Get()->Write(FromGeo, LogNotice, "Geocoded: %s, %s (%.4f, %.4f)",
                              outLocation->city,
                              outLocation->stateAbbrev[0] ? outLocation->stateAbbrev : outLocation->state,
                              outLocation->latitude, outLocation->longitude);
        return true;
    }

    CLogger::Get()->Write(FromGeo, LogError, "Incomplete geocoding data");
    return false;
}

bool GeocodingService::ExtractString(const char* json, const char* key, char* outValue, size_t maxLen)
{
    const char* pos = strstr(json, key);
    if (!pos) return false;

    pos = strchr(pos, ':');
    if (!pos) return false;

    // Skip whitespace
    pos++;
    while (*pos == ' ' || *pos == '\t') pos++;

    // Check for quoted string
    if (*pos != '"') return false;

    pos++; // Skip opening quote
    const char* end = strchr(pos, '"');
    if (!end) return false;

    size_t len = end - pos;
    if (len >= maxLen) len = maxLen - 1;

    strncpy(outValue, pos, len);
    outValue[len] = '\0';
    return true;
}

bool GeocodingService::ExtractFloat(const char* json, const char* key, float* outValue)
{
    const char* pos = strstr(json, key);
    if (!pos) return false;

    pos = strchr(pos, ':');
    if (!pos) return false;

    pos++; // Skip ':'
    while (*pos == ' ' || *pos == '\t') pos++;

    *outValue = strtof(pos, NULL);
    return true;
}

} // namespace mm
