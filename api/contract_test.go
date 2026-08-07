package api

import (
	"encoding/json"
	"os"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/MingYuan0415/mt-server/internal/modules/weather"
)

func TestOpenAPIContract(t *testing.T) {
	contents, err := os.ReadFile("openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	if document["openapi"] != "3.1.0" {
		t.Fatalf("unexpected OpenAPI version %#v", document["openapi"])
	}
	paths := document["paths"].(map[string]any)
	weatherPaths := []string{
		"/api/v1/weather/current",
		"/api/v1/weather/hourly",
		"/api/v1/weather/daily",
		"/api/v1/weather/alerts",
	}
	for _, path := range append(weatherPaths,
		"/api/v1/location",
		"/health/live",
		"/health/ready",
	) {
		if _, ok := paths[path]; !ok {
			t.Errorf("missing path %s", path)
		}
	}
	for _, path := range weatherPaths {
		operation := paths[path].(map[string]any)["get"].(map[string]any)
		responses := operation["responses"].(map[string]any)
		for _, status := range []string{"200", "400", "401", "429", "503", "504"} {
			if _, ok := responses[status]; !ok {
				t.Errorf("weather path %s has no %s response", path, status)
			}
		}
		checkLocationParameters(t, document, operation["parameters"].([]any), "weather path "+path)
		content := responses["200"].(map[string]any)["content"].(map[string]any)
		mediaType := content["application/json"].(map[string]any)
		if _, ok := mediaType["examples"]; !ok {
			t.Errorf("weather path %s has no response example", path)
		}
	}
	locationPath := paths["/api/v1/location"].(map[string]any)["get"].(map[string]any)
	locationResponses := locationPath["responses"].(map[string]any)
	for _, status := range []string{"200", "400", "401", "503"} {
		if _, ok := locationResponses[status]; !ok {
			t.Errorf("location path has no %s response", status)
		}
	}
	if parameters, exists := locationPath["parameters"]; !exists {
		t.Error("location path must accept the same seven optional location parameters")
	} else {
		checkLocationParameters(t, document, parameters.([]any), "location path")
	}
	components := document["components"].(map[string]any)
	parameters := components["parameters"].(map[string]any)
	for _, name := range []string{
		"LocationLatitude", "LocationLongitude", "LocationProvider", "LocationCity",
		"LocationRegion", "LocationCountry", "LocationTimezone",
	} {
		if _, ok := parameters[name]; !ok {
			t.Errorf("missing location parameter %s", name)
		}
	}
	examples := components["examples"].(map[string]any)
	if len(examples) != len(weatherPaths)+1 {
		t.Fatalf("expected %d examples, got %d", len(weatherPaths)+1, len(examples))
	}
	exampleJSON, err := json.Marshal(examples)
	if err != nil {
		t.Fatal(err)
	}
	locationKeyPattern := regexp.MustCompile(`^[a-f0-9]{16}$`)
	for name, example := range examples {
		value, ok := example.(map[string]any)["value"].(map[string]any)
		if !ok {
			t.Fatalf("example %s has no value", name)
		}
		location, ok := value["location"].(map[string]any)
		if !ok {
			t.Fatalf("example %s has no location object", name)
		}
		key, ok := location["location_key"].(string)
		if !ok || !locationKeyPattern.MatchString(key) {
			t.Errorf("example %s location_key must be 16 lowercase hex digits, got %#v",
				name, key)
		}
	}
	for _, required := range []string{
		`"id":"qweather"`, `"name":"QWeather"`, "https://www.qweather.com/",
		`"source":"device"`, `"provider":"example"`, `"truncated":false`,
		`"source":"ip"`, `"provider":"maxmind"`, `"accuracy_radius_km":50`,
		`"location_key":"9f4a2b3c8d1e5f06"`,
	} {
		if !strings.Contains(string(exampleJSON), required) {
			t.Errorf("OpenAPI examples are missing %q", required)
		}
	}
	for _, forbidden := range []string{"latitude", "longitude", "192.168.", "203.0.113."} {
		if strings.Contains(string(exampleJSON), forbidden) {
			t.Errorf("OpenAPI examples contain private location field %q", forbidden)
		}
	}
	if strings.Contains(string(contents), "192.168.") {
		t.Fatal("OpenAPI document contains a private deployment address")
	}
}

func TestWeatherModelsMatchRequiredContractFields(t *testing.T) {
	document := loadDocument(t, "openapi.json")
	schemas := document["components"].(map[string]any)["schemas"].(map[string]any)
	now := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	values := map[string]any{
		"CurrentWeather": weather.Current{ObservedAt: now},
		"HourlyItem":     weather.Hour{ForecastAt: now},
		"DailyItem":      weather.Day{Date: "2026-08-03"},
		"WeatherAlerts":  weather.Alerts{Items: []weather.Alert{}},
		"WeatherAlert": weather.Alert{ID: "id", Title: "title", TypeCode: "type",
			TypeName: "name", Severity: "unknown", Status: "unknown", IssuedAt: now,
			Urgency: "unknown", Certainty: "unknown"},
	}
	for schemaName, value := range values {
		contents, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		var object map[string]any
		if err := json.Unmarshal(contents, &object); err != nil {
			t.Fatal(err)
		}
		for _, field := range stringSlice(schemas[schemaName].(map[string]any)["required"]) {
			if _, ok := object[field]; !ok {
				t.Errorf("%s JSON is missing required field %s: %s", schemaName, field, contents)
			}
		}
	}
}

func TestAdminOpenAPIContract(t *testing.T) {
	contents, err := os.ReadFile("admin-openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	paths := document["paths"].(map[string]any)
	for _, path := range []string{
		"/status", "/setup", "/session", "/settings/qweather",
		"/settings/qweather/test", "/device-tokens",
		"/device-tokens/{id}", "/account/password", "/diagnostics",
	} {
		if _, ok := paths[path]; !ok {
			t.Errorf("missing management path %s", path)
		}
	}
	if _, exists := paths["/settings/location"]; exists {
		t.Fatal("removed location settings path is still documented")
	}
	for _, forbidden := range []string{"192.168.", "PRIVATE KEY-----"} {
		if strings.Contains(string(contents), forbidden) {
			t.Errorf("management OpenAPI contains deployment data %q", forbidden)
		}
	}
	for _, forbidden := range []string{"setupToken", "Setup <one-time-token>", "bootstrap.json"} {
		if strings.Contains(string(contents), forbidden) {
			t.Errorf("management OpenAPI still contains removed setup credential %q", forbidden)
		}
	}
	for _, required := range []string{`"test_location"`, `"Verification"`, `"verification"`,
		`"tested_capabilities"`, `"DiagnosticsResponse"`} {
		if !strings.Contains(string(contents), required) {
			t.Errorf("management OpenAPI is missing %q", required)
		}
	}
	setup := paths["/setup"].(map[string]any)["post"].(map[string]any)
	security := setup["security"].([]any)
	if len(security) != 0 {
		t.Fatalf("setup still declares credential security: %#v", security)
	}
}

func loadDocument(t *testing.T, name string) map[string]any {
	t.Helper()
	contents, err := os.ReadFile(name)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err := json.Unmarshal(contents, &document); err != nil {
		t.Fatal(err)
	}
	return document
}

// checkLocationParameters resolves parameter $refs and verifies the exact set
// of seven optional location-header parameters, independent of ordering.
func checkLocationParameters(t *testing.T, document map[string]any,
	values []any, label string) {
	t.Helper()
	components := document["components"].(map[string]any)["parameters"].(map[string]any)
	got := make(map[string]bool, len(values))
	for _, value := range values {
		entry, ok := value.(map[string]any)
		if !ok {
			t.Errorf("%s has a non-object parameter", label)
			continue
		}
		name, in, required, valid := resolveParameter(t, components, entry)
		if !valid {
			continue
		}
		key := name + "\x00" + in
		if got[key] {
			t.Errorf("%s duplicates parameter %s", label, name)
		}
		got[key] = true
		if required {
			t.Errorf("%s must not require location header %s", label, name)
		}
	}
	expected := []string{
		"X-MT-Location-Latitude\x00header",
		"X-MT-Location-Longitude\x00header",
		"X-MT-Location-Provider\x00header",
		"X-MT-Location-City\x00header",
		"X-MT-Location-Region\x00header",
		"X-MT-Location-Country\x00header",
		"X-MT-Location-Timezone\x00header",
	}
	for _, key := range expected {
		if !got[key] {
			t.Errorf("%s is missing location header %q", label, key)
		}
	}
}

func resolveParameter(t *testing.T, components map[string]any,
	entry map[string]any) (name, in string, required, valid bool) {
	t.Helper()
	if reference, ok := entry["$ref"].(string); ok {
		componentName, found := strings.CutPrefix(reference, "#/components/parameters/")
		if !found {
			t.Errorf("invalid parameter reference %q", reference)
			return "", "", false, false
		}
		resolved, exists := components[componentName]
		if !exists {
			t.Errorf("referenced parameter %s is missing", componentName)
			return "", "", false, false
		}
		entry = resolved.(map[string]any)
	}
	name, _ = entry["name"].(string)
	in, _ = entry["in"].(string)
	required = entry["required"] == true
	return name, in, required, true
}

func stringSlice(value any) []string {
	items, _ := value.([]any)
	result := make([]string, 0, len(items))
	for _, item := range items {
		if text, ok := item.(string); ok {
			result = append(result, text)
		}
	}
	return result
}
