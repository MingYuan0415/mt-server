// Package weather implements normalized weather data and caching.
package weather

import "time"

// Kind identifies an independently cached weather dataset.
type Kind string

const (
	KindCurrent Kind = "current"
	KindHourly  Kind = "hourly"
	KindDaily   Kind = "daily"
	KindAlerts  Kind = "alerts"
)

// Source identifies the data provider for attribution.
type Source struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	AttributionURL string `json:"attribution_url"`
}

// PublicLocation contains coarse display metadata only.
type PublicLocation struct {
	City      string `json:"city,omitempty"`
	District  string `json:"district,omitempty"`
	Region    string `json:"region,omitempty"`
	Country   string `json:"country,omitempty"`
	Timezone  string `json:"timezone,omitempty"`
	Source    string `json:"source"`
	Provider  string `json:"provider"`
	Precision string `json:"precision"`
	// LocationKey is the opaque scope identity derived from the normalized
	// two-decimal coordinates. It never contains coordinates or the IP.
	LocationKey string `json:"location_key,omitempty"`
}

// Envelope is the stable device-facing response shape.
type Envelope struct {
	SchemaVersion int            `json:"schema_version"`
	Source        Source         `json:"source"`
	Location      PublicLocation `json:"location"`
	FetchedAt     time.Time      `json:"fetched_at"`
	UpdatedAt     time.Time      `json:"updated_at"`
	ValidUntil    time.Time      `json:"valid_until"`
	Stale         bool           `json:"stale"`
	Data          any            `json:"data"`
}

// Verification is an uncached current-weather result for the management UI.
type Verification struct {
	Source    Source         `json:"source"`
	Location  PublicLocation `json:"location"`
	TestedAt  time.Time      `json:"tested_at"`
	UpdatedAt time.Time      `json:"updated_at"`
	Data      Current        `json:"data"`
}

// Current is a normalized current observation in metric units.
type Current struct {
	ObservedAt      time.Time `json:"observed_at"`
	TemperatureC    float64   `json:"temperature_c"`
	FeelsLikeC      float64   `json:"feels_like_c"`
	ConditionCode   string    `json:"condition_code"`
	ConditionText   string    `json:"condition_text"`
	WindDegrees     float64   `json:"wind_degrees"`
	WindDirection   string    `json:"wind_direction"`
	WindScale       string    `json:"wind_scale"`
	WindSpeedKMH    float64   `json:"wind_speed_kmh"`
	HumidityPercent float64   `json:"humidity_percent"`
	PrecipitationMM float64   `json:"precipitation_mm"`
	PressureHPA     float64   `json:"pressure_hpa"`
	VisibilityKM    float64   `json:"visibility_km"`
	CloudPercent    *float64  `json:"cloud_percent,omitempty"`
	DewPointC       *float64  `json:"dew_point_c,omitempty"`
}

// Hourly contains the next 24 hours.
type Hourly struct {
	Hours []Hour `json:"hours"`
}

// Hour is one normalized hourly forecast.
type Hour struct {
	ForecastAt          time.Time `json:"forecast_at"`
	TemperatureC        float64   `json:"temperature_c"`
	ConditionCode       string    `json:"condition_code"`
	ConditionText       string    `json:"condition_text"`
	WindDegrees         float64   `json:"wind_degrees"`
	WindDirection       string    `json:"wind_direction"`
	WindScale           string    `json:"wind_scale"`
	WindSpeedKMH        float64   `json:"wind_speed_kmh"`
	HumidityPercent     float64   `json:"humidity_percent"`
	PrecipitationChance float64   `json:"precipitation_chance_percent"`
	PrecipitationMM     float64   `json:"precipitation_mm"`
	PressureHPA         float64   `json:"pressure_hpa"`
	CloudPercent        *float64  `json:"cloud_percent,omitempty"`
	DewPointC           *float64  `json:"dew_point_c,omitempty"`
}

// Daily contains the next seven days.
type Daily struct {
	Days []Day `json:"days"`
}

// Day is one normalized daily forecast.
type Day struct {
	Date               string     `json:"date"`
	Sunrise            *time.Time `json:"sunrise,omitempty"`
	Sunset             *time.Time `json:"sunset,omitempty"`
	Moonrise           *time.Time `json:"moonrise,omitempty"`
	Moonset            *time.Time `json:"moonset,omitempty"`
	MoonPhase          string     `json:"moon_phase,omitempty"`
	MoonPhaseCode      string     `json:"moon_phase_code,omitempty"`
	TemperatureMaxC    float64    `json:"temperature_max_c"`
	TemperatureMinC    float64    `json:"temperature_min_c"`
	ConditionDayCode   string     `json:"condition_day_code"`
	ConditionDayText   string     `json:"condition_day_text"`
	ConditionNightCode string     `json:"condition_night_code"`
	ConditionNightText string     `json:"condition_night_text"`
	WindDayDegrees     float64    `json:"wind_day_degrees"`
	WindDayDirection   string     `json:"wind_day_direction"`
	WindDayScale       string     `json:"wind_day_scale"`
	WindDaySpeedKMH    float64    `json:"wind_day_speed_kmh"`
	WindNightDegrees   float64    `json:"wind_night_degrees"`
	WindNightDirection string     `json:"wind_night_direction"`
	WindNightScale     string     `json:"wind_night_scale"`
	WindNightSpeedKMH  float64    `json:"wind_night_speed_kmh"`
	HumidityPercent    float64    `json:"humidity_percent"`
	PrecipitationMM    float64    `json:"precipitation_mm"`
	PressureHPA        float64    `json:"pressure_hpa"`
	VisibilityKM       float64    `json:"visibility_km"`
	CloudPercent       *float64   `json:"cloud_percent,omitempty"`
	UVIndex            float64    `json:"uv_index"`
}

// Alerts is the complete warning snapshot returned by the provider.
type Alerts struct {
	DetailURL string  `json:"detail_url,omitempty"`
	Truncated bool    `json:"truncated"`
	Items     []Alert `json:"items"`
}

// Alert is one normalized weather warning.
type Alert struct {
	ID               string     `json:"id"`
	Title            string     `json:"title"`
	IssuingAuthority string     `json:"issuing_authority,omitempty"`
	TypeCode         string     `json:"type_code"`
	TypeName         string     `json:"type_name"`
	Severity         string     `json:"severity"`
	Status           string     `json:"status"`
	IssuedAt         time.Time  `json:"issued_at"`
	StartsAt         *time.Time `json:"starts_at,omitempty"`
	EndsAt           *time.Time `json:"ends_at,omitempty"`
	Urgency          string     `json:"urgency"`
	Certainty        string     `json:"certainty"`
	Description      string     `json:"description,omitempty"`
	Instruction      string     `json:"instruction,omitempty"`
	ContentTruncated bool       `json:"content_truncated"`
}

// ProviderResult carries normalized data and the provider update time.
type ProviderResult struct {
	UpdatedAt time.Time
	Data      any
}
