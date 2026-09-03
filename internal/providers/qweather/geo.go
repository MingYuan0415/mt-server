package qweather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/MingYuan0415/mt-server/internal/platform/location"
)

// maximumLocalizedNameRunes matches the platform display-metadata bound.
const maximumLocalizedNameRunes = 128

// geoLookupResponse is the private GeoAPI city-lookup shape. Raw GeoAPI data
// never leaves this package and is never cached or persisted.
type geoLookupResponse struct {
	Location []struct {
		Name string `json:"name"`
		Adm1 string `json:"adm1"`
		Adm2 string `json:"adm2"`
		Tz   string `json:"tz"`
	} `json:"location"`
}

const geoLookupPath = "/geo/v2/city/lookup"

// Localize resolves Simplified-Chinese display names for a normalized point
// through the GeoAPI city-lookup endpoint. The request uses only the
// normalized two-decimal coordinates, and shares the provider-wide
// concurrency limit with weather fetches. Failures must be treated as
// best-effort by callers, which fall back to the original display metadata.
func (c *Client) Localize(ctx context.Context, point location.Point) (location.LocalizedMetadata, error) {
	if err := c.acquire(ctx); err != nil {
		return location.LocalizedMetadata{}, err
	}
	defer func() { <-c.slots }()

	query := url.Values{}
	query.Set("location", strconv.FormatFloat(point.Longitude, 'f', 2, 64)+","+
		strconv.FormatFloat(point.Latitude, 'f', 2, 64))
	query.Set("number", "1")
	query.Set("lang", c.language)
	body, err := c.requestWithQuery(ctx, geoLookupPath, query, false)
	if err != nil {
		return location.LocalizedMetadata{}, err
	}
	var response geoLookupResponse
	if err := json.Unmarshal(body, &response); err != nil {
		return location.LocalizedMetadata{}, fmt.Errorf("decode qweather geo response: %w", err)
	}
	if len(response.Location) == 0 {
		return location.LocalizedMetadata{}, errors.New("qweather geo response has no location")
	}
	matched := response.Location[0]
	metadata := location.LocalizedMetadata{
		City: matched.Adm2, District: matched.Name,
		Region: matched.Adm1, Timezone: matched.Tz,
	}
	// A missing secondary administrative area (rare) degrades city to the
	// matched locality name so the city field never loses its value.
	if strings.TrimSpace(metadata.City) == "" {
		metadata.City = metadata.District
	}
	valid := 0
	for _, field := range []*string{&metadata.City, &metadata.District, &metadata.Region, &metadata.Timezone} {
		if value, ok := validLocalizedName(*field); ok {
			*field = value
			valid++
		} else {
			*field = ""
		}
	}
	if valid == 0 {
		return location.LocalizedMetadata{}, errors.New("qweather geo response has no usable names")
	}
	return metadata, nil
}

// validLocalizedName normalizes a GeoAPI display name so it is safe to
// overlay: trimmed, valid UTF-8, no control characters, and within the
// platform display-metadata bound. It returns the normalized value.
func validLocalizedName(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "" || !utf8.ValidString(value) ||
		len([]rune(value)) > maximumLocalizedNameRunes {
		return "", false
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return "", false
		}
	}
	return value, true
}
