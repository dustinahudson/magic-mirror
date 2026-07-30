package source

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// Keys this package publishes under.
const (
	KeyWeather  = "weather"
	KeyForecast = "forecast"
)

// Conditions is the current weather.
type Conditions struct {
	Temperature   float64   `json:"temperature"`
	FeelsLike     float64   `json:"feelsLike"`
	Humidity      int       `json:"humidity"`
	WindSpeed     float64   `json:"windSpeed"`
	WindDirection int       `json:"windDirection"`
	Code          int       `json:"code"`
	IsDay         bool      `json:"isDay"`
	Sunrise       time.Time `json:"sunrise"`
	Sunset        time.Time `json:"sunset"`
	City          string    `json:"city"`
	Region        string    `json:"region"`
	Metric        bool      `json:"metric"`

	// Forecast rides along with current conditions because Open-Meteo
	// returns both from one request — one round trip instead of the two v1
	// made.
	//
	// Index 0 is today, with today's real high and low.
	Forecast []ForecastDay `json:"forecast"`
}

// Condition returns a human-readable description of the weather code.
func (c Conditions) Condition() string { return WMODescription(c.Code) }

// Icon returns the embedded icon name for the current conditions.
func (c Conditions) Icon() string { return WMOIcon(c.Code, c.IsDay) }

// ForecastDay is one day of the outlook.
type ForecastDay struct {
	Date time.Time `json:"date"`
	High float64   `json:"high"`
	Low  float64   `json:"low"`
	Code int       `json:"code"`
}

// Icon returns the embedded icon name for the day. Forecast rows always use
// daytime artwork.
func (d ForecastDay) Icon() string { return WMOIcon(d.Code, true) }

// Condition describes the day's weather.
func (d ForecastDay) Condition() string { return WMODescription(d.Code) }

// Location is a resolved place.
type Location struct {
	Latitude  float64
	Longitude float64
	City      string
	Region    string
}

// WeatherSource fetches current conditions and the forecast from Open-Meteo.
//
// Geocoding happens here rather than in a separate fetcher because the
// lookup is a precondition for the fetch, not a parallel concern: without
// coordinates there is no weather request to make. The result is cached for
// the process lifetime — a zipcode does not move.
type WeatherSource struct {
	Zipcode   string
	Metric    bool
	Interval_ time.Duration

	// Fixed short-circuits geocoding when the config supplies coordinates.
	Fixed *Location

	client   *http.Client
	resolved *Location
}

// NewWeather returns a weather fetcher.
func NewWeather(zipcode string, metric bool, fixed *Location, interval time.Duration) *WeatherSource {
	if interval <= 0 {
		interval = 15 * time.Minute
	}
	return &WeatherSource{
		Zipcode:   zipcode,
		Metric:    metric,
		Fixed:     fixed,
		Interval_: interval,
		client:    HTTPClient(20 * time.Second),
	}
}

func (w *WeatherSource) Key() string             { return KeyWeather }
func (w *WeatherSource) Interval() time.Duration { return w.Interval_ }
func (w *WeatherSource) Timeout() time.Duration  { return 25 * time.Second }

func (w *WeatherSource) Fetch(ctx context.Context) (any, error) {
	loc, err := w.location(ctx)
	if err != nil {
		return nil, err
	}

	tempUnit, windUnit := "fahrenheit", "mph"
	if w.Metric {
		tempUnit, windUnit = "celsius", "kmh"
	}

	q := url.Values{}
	q.Set("latitude", fmt.Sprintf("%.4f", loc.Latitude))
	q.Set("longitude", fmt.Sprintf("%.4f", loc.Longitude))
	q.Set("current", "temperature_2m,apparent_temperature,relative_humidity_2m,"+
		"weather_code,wind_speed_10m,wind_direction_10m,is_day")
	q.Set("daily", "weather_code,temperature_2m_max,temperature_2m_min,sunrise,sunset")
	q.Set("temperature_unit", tempUnit)
	q.Set("wind_speed_unit", windUnit)
	q.Set("timezone", "auto")
	q.Set("forecast_days", "6")

	endpoint := "https://api.open-meteo.com/v1/forecast?" + q.Encode()

	var resp struct {
		UTCOffset int `json:"utc_offset_seconds"`
		Current   struct {
			Temperature   float64 `json:"temperature_2m"`
			Apparent      float64 `json:"apparent_temperature"`
			Humidity      int     `json:"relative_humidity_2m"`
			Code          int     `json:"weather_code"`
			WindSpeed     float64 `json:"wind_speed_10m"`
			WindDirection int     `json:"wind_direction_10m"`
			IsDay         int     `json:"is_day"`
		} `json:"current"`
		Daily struct {
			Time    []string  `json:"time"`
			Code    []int     `json:"weather_code"`
			Max     []float64 `json:"temperature_2m_max"`
			Min     []float64 `json:"temperature_2m_min"`
			Sunrise []string  `json:"sunrise"`
			Sunset  []string  `json:"sunset"`
		} `json:"daily"`
	}
	if err := getJSON(ctx, w.client, endpoint, &resp); err != nil {
		return nil, err
	}

	zone := time.FixedZone("local", resp.UTCOffset)
	out := Conditions{
		Temperature:   resp.Current.Temperature,
		FeelsLike:     resp.Current.Apparent,
		Humidity:      resp.Current.Humidity,
		WindSpeed:     resp.Current.WindSpeed,
		WindDirection: resp.Current.WindDirection,
		Code:          resp.Current.Code,
		IsDay:         resp.Current.IsDay == 1,
		City:          loc.City,
		Region:        loc.Region,
		Metric:        w.Metric,
	}

	if len(resp.Daily.Sunrise) > 0 {
		out.Sunrise, _ = time.ParseInLocation("2006-01-02T15:04", resp.Daily.Sunrise[0], zone)
	}
	if len(resp.Daily.Sunset) > 0 {
		out.Sunset, _ = time.ParseInLocation("2006-01-02T15:04", resp.Daily.Sunset[0], zone)
	}

	// Today is index 0 and is kept.
	//
	// It was previously dropped, on the grounds that current conditions
	// already cover today — but v1's forecast opened with a "Today" row, and
	// reconstructing one from the current temperature meant printing
	// "82° / 82°", which looks like a real range and is not. Open-Meteo
	// returns today's actual high and low; consumers that want only the days
	// ahead can skip the first entry.
	for i := 0; i < len(resp.Daily.Time); i++ {
		if i >= len(resp.Daily.Code) || i >= len(resp.Daily.Max) || i >= len(resp.Daily.Min) {
			break
		}
		day, err := time.ParseInLocation("2006-01-02", resp.Daily.Time[i], zone)
		if err != nil {
			continue
		}
		out.Forecast = append(out.Forecast, ForecastDay{
			Date: day,
			High: resp.Daily.Max[i],
			Low:  resp.Daily.Min[i],
			Code: resp.Daily.Code[i],
		})
	}

	return out, nil
}

// location resolves coordinates, preferring explicit config, then a cached
// lookup, then the geocoder.
func (w *WeatherSource) location(ctx context.Context) (Location, error) {
	if w.Fixed != nil {
		return *w.Fixed, nil
	}
	if w.resolved != nil {
		return *w.resolved, nil
	}
	if strings.TrimSpace(w.Zipcode) == "" {
		return Location{}, fmt.Errorf("no zipcode or coordinates configured")
	}

	loc, err := Geocode(ctx, w.client, w.Zipcode)
	if err != nil {
		return Location{}, err
	}
	w.resolved = &loc
	return loc, nil
}

// Geocode resolves a place name or postal code to coordinates.
//
// Same endpoint v1 used (geocoding_service.cpp:35).
func Geocode(ctx context.Context, client *http.Client, query string) (Location, error) {
	q := url.Values{}
	q.Set("name", query)
	q.Set("count", "1")
	q.Set("language", "en")
	q.Set("format", "json")

	endpoint := "https://geocoding-api.open-meteo.com/v1/search?" + q.Encode()

	var resp struct {
		Results []struct {
			Latitude  float64 `json:"latitude"`
			Longitude float64 `json:"longitude"`
			Name      string  `json:"name"`
			Admin1    string  `json:"admin1"`
			Country   string  `json:"country_code"`
		} `json:"results"`
	}
	if err := getJSON(ctx, client, endpoint, &resp); err != nil {
		return Location{}, fmt.Errorf("geocode %q: %w", query, err)
	}
	if len(resp.Results) == 0 {
		return Location{}, fmt.Errorf("geocode %q: no results", query)
	}

	r := resp.Results[0]
	region := r.Admin1
	if region == "" {
		region = r.Country
	}
	return Location{
		Latitude:  r.Latitude,
		Longitude: r.Longitude,
		City:      r.Name,
		Region:    region,
	}, nil
}

// getJSON performs a GET and decodes the body, honouring ctx throughout.
func getJSON(ctx context.Context, client *http.Client, endpoint string, out any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "magic-mirror/2")

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return &ErrHTTPStatus{URL: endpoint, Status: resp.StatusCode}
	}
	if err := json.NewDecoder(resp.Body).Decode(out); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	return nil
}

// WMOIcon maps a WMO weather code to one of the embedded icon names.
//
// Codes are from https://open-meteo.com/en/docs; the icon set is the one v1
// shipped, now as PNGs under assets/icons rather than generated C arrays.
func WMOIcon(code int, day bool) string {
	suffix := func(base string) string {
		if day {
			return base + "_day"
		}
		return base + "_night"
	}

	switch code {
	case 0:
		return suffix("clear")
	case 1, 2:
		return suffix("partly_cloudy")
	case 3:
		return "cloudy"
	case 45, 48:
		return "fog"
	case 51, 53, 55, 56, 57:
		return "drizzle"
	case 61, 63, 65, 66, 67, 80, 81, 82:
		return "rain"
	case 71, 73, 75, 77, 85, 86:
		return "snow"
	case 95, 96, 99:
		return "thunderstorm"
	default:
		return "cloudy"
	}
}

// WMODescription gives a short human-readable label for a weather code.
func WMODescription(code int) string {
	switch code {
	case 0:
		return "Clear"
	case 1:
		return "Mostly Clear"
	case 2:
		return "Partly Cloudy"
	case 3:
		return "Overcast"
	case 45, 48:
		return "Fog"
	case 51:
		return "Light Drizzle"
	case 53:
		return "Drizzle"
	case 55:
		return "Heavy Drizzle"
	case 56, 57:
		return "Freezing Drizzle"
	case 61:
		return "Light Rain"
	case 63:
		return "Rain"
	case 65:
		return "Heavy Rain"
	case 66, 67:
		return "Freezing Rain"
	case 71:
		return "Light Snow"
	case 73:
		return "Snow"
	case 75:
		return "Heavy Snow"
	case 77:
		return "Snow Grains"
	case 80:
		return "Light Showers"
	case 81:
		return "Showers"
	case 82:
		return "Heavy Showers"
	case 85, 86:
		return "Snow Showers"
	case 95:
		return "Thunderstorm"
	case 96, 99:
		return "Thunderstorm, Hail"
	default:
		return "Unknown"
	}
}
