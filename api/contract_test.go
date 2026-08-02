package api

import (
	"encoding/json"
	"os"
	"strings"
	"testing"
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
	}
	for _, path := range append(weatherPaths,
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
		parameters := operation["parameters"].([]any)
		if len(parameters) != 7 {
			t.Errorf("weather path %s has %d location parameters", path, len(parameters))
		}
		content := responses["200"].(map[string]any)["content"].(map[string]any)
		mediaType := content["application/json"].(map[string]any)
		if _, ok := mediaType["examples"]; !ok {
			t.Errorf("weather path %s has no response example", path)
		}
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
	if len(examples) != len(weatherPaths) {
		t.Fatalf("expected %d weather examples, got %d", len(weatherPaths), len(examples))
	}
	exampleJSON, err := json.Marshal(examples)
	if err != nil {
		t.Fatal(err)
	}
	for _, required := range []string{
		`"id":"qweather"`, `"name":"QWeather"`, "https://www.qweather.com/",
		`"source":"device"`, `"provider":"ipinfo"`,
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
		"/device-tokens/{id}", "/account/password",
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
	for _, required := range []string{`"test_location"`, `"Verification"`, `"verification"`} {
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
