package api

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"os"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
)

var schemaConstraintKeywords = []string{
	"type", "format", "const", "minimum", "maximum", "exclusiveMinimum",
	"exclusiveMaximum", "multipleOf", "minLength", "maxLength", "pattern",
	"minItems", "maxItems", "uniqueItems", "minProperties", "maxProperties",
	"minContains", "maxContains", "contains", "prefixItems", "unevaluatedItems",
	"propertyNames", "patternProperties", "dependentRequired", "dependentSchemas",
	"unevaluatedProperties", "not", "if", "then", "else", "discriminator",
	"nullable", "readOnly", "writeOnly", "contentEncoding", "contentMediaType",
	"unit", "x-unit",
}

func TestV020BaselineFixtureUnchanged(t *testing.T) {
	contents, err := os.ReadFile("testdata/openapi-v0.2.0.json")
	if err != nil {
		t.Fatal(err)
	}
	const expected = "bb47a1322432a27416adc2de0d0a78b6677b5cd0b6dbe755435d4d0e2fb4b638"
	if actual := fmt.Sprintf("%x", sha256.Sum256(contents)); actual != expected {
		t.Fatalf("v0.2.0 OpenAPI baseline checksum changed: got %s want %s", actual, expected)
	}
}

func TestWeatherV1BaselineRemainsAdditive(t *testing.T) {
	baseline := loadDocument(t, "testdata/openapi-v0.2.0.json")
	current := loadDocument(t, "openapi.json")
	if failures := checkV1Compatibility(baseline, current); len(failures) != 0 {
		t.Fatalf("v1 contract is not additive:\n%s", strings.Join(failures, "\n"))
	}
}

func TestV1CompatibilityRules(t *testing.T) {
	baseline := loadDocument(t, "testdata/openapi-v0.2.0.json")
	tests := []struct {
		name     string
		mutate   func(map[string]any)
		wantPath string
	}{
		{
			name: "deleted response field",
			mutate: func(document map[string]any) {
				delete(schemaProperties(document, "CurrentWeather"), "temperature_c")
			},
			wantPath: `$["components"]["schemas"]["CurrentWeather"]["properties"]["temperature_c"]`,
		},
		{
			name: "changed response type",
			mutate: func(document map[string]any) {
				schemaProperties(document, "CurrentWeather")["temperature_c"].(map[string]any)["type"] = "string"
			},
			wantPath: `$["components"]["schemas"]["CurrentWeather"]["properties"]["temperature_c"]["type"]`,
		},
		{
			name: "new required request header",
			mutate: func(document map[string]any) {
				operation := weatherOperation(document, "/api/v1/weather/current")
				operation["parameters"] = append(operation["parameters"].([]any), map[string]any{
					"name": "X-MT-New-Required", "in": "header", "required": true,
					"schema": map[string]any{"type": "string"},
				})
			},
			wantPath: `$["paths"]["/api/v1/weather/current"]["get"]["parameters"][7]`,
		},
		{
			name: "new path-level required request header",
			mutate: func(document map[string]any) {
				pathItem := weatherPathItem(document, "/api/v1/weather/current")
				pathItem["parameters"] = []any{map[string]any{
					"name": "X-MT-New-Path-Required", "in": "header", "required": true,
					"schema": map[string]any{"type": "string"},
				}}
			},
			wantPath: `$["paths"]["/api/v1/weather/current"]["parameters"][0]`,
		},
		{
			name: "new allOf constraint",
			mutate: func(document map[string]any) {
				schema := responseSchema(document, "/api/v1/weather/current", "200")
				schema["allOf"] = append(schema["allOf"].([]any), map[string]any{
					"properties": map[string]any{
						"data": map[string]any{"maxProperties": 1},
					},
				})
			},
			wantPath: `$["paths"]["/api/v1/weather/current"]["get"]["responses"]["200"]["content"]["application/json"]["schema"]["allOf"][2]`,
		},
		{
			name: "new ref sibling constraint",
			mutate: func(document map[string]any) {
				schemaProperties(document, "WeatherEnvelope")["source"].(map[string]any)["maxProperties"] = 1
			},
			wantPath: `$["components"]["schemas"]["WeatherEnvelope"]["properties"]["source"]["maxProperties"]`,
		},
		{
			name: "new ref sibling property constraint",
			mutate: func(document map[string]any) {
				schemaProperties(document, "WeatherEnvelope")["source"].(map[string]any)["properties"] = map[string]any{
					"id": map[string]any{"maxLength": 1},
				}
			},
			wantPath: `$["components"]["schemas"]["WeatherEnvelope"]["properties"]["source"]["properties"]["id"]`,
		},
		{
			name: "new ref sibling required constraint",
			mutate: func(document map[string]any) {
				schemaProperties(document, "WeatherEnvelope")["location"].(map[string]any)["required"] = []any{"region"}
			},
			wantPath: `$["components"]["schemas"]["WeatherEnvelope"]["properties"]["location"]["required"]["region"]`,
		},
		{
			name: "existing optional response field made required",
			mutate: func(document map[string]any) {
				schema := document["components"].(map[string]any)["schemas"].(map[string]any)["CurrentWeather"].(map[string]any)
				schema["required"] = append(schema["required"].([]any), "dew_point_c")
			},
			wantPath: `$["components"]["schemas"]["CurrentWeather"]["required"]["dew_point_c"]`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			candidate := cloneDocument(t, baseline)
			test.mutate(candidate)
			failures := checkV1Compatibility(baseline, candidate)
			if !slices.ContainsFunc(failures, func(value string) bool {
				return strings.Contains(value, test.wantPath)
			}) {
				t.Fatalf("expected failure at %s, got %#v", test.wantPath, failures)
			}
		})
	}
}

func TestV1CompatibilityAllowsAdditions(t *testing.T) {
	baseline := loadDocument(t, "testdata/openapi-v0.2.0.json")
	candidate := cloneDocument(t, baseline)
	candidate["paths"].(map[string]any)["/api/v1/weather/alerts"] = map[string]any{
		"get": map[string]any{"responses": map[string]any{"200": map[string]any{"description": "Alerts"}}},
	}
	schemaProperties(candidate, "CurrentWeather")["new_optional_field"] = map[string]any{"type": "string"}
	weatherOperation(candidate, "/api/v1/weather/current")["responses"].(map[string]any)["206"] =
		map[string]any{"description": "Additional response"}
	if failures := checkV1Compatibility(baseline, candidate); len(failures) != 0 {
		t.Fatalf("additive changes were rejected:\n%s", strings.Join(failures, "\n"))
	}
}

func TestV1CompatibilityUsesEffectivePathParameters(t *testing.T) {
	baseline := loadDocument(t, "testdata/openapi-v0.2.0.json")
	candidate := cloneDocument(t, baseline)
	pathItem := weatherPathItem(candidate, "/api/v1/weather/current")
	operation := weatherOperation(candidate, "/api/v1/weather/current")
	parameters := operation["parameters"].([]any)
	pathItem["parameters"] = []any{parameters[0]}
	operation["parameters"] = parameters[1:]
	if failures := checkV1Compatibility(baseline, candidate); len(failures) != 0 {
		t.Fatalf("moving an unchanged effective parameter was rejected:\n%s", strings.Join(failures, "\n"))
	}
}

func checkV1Compatibility(baseline, current map[string]any) []string {
	checker := compatibilityChecker{baseline: baseline, current: current}
	checker.comparePaths()
	checker.compareComponents()
	return checker.failures
}

type compatibilityChecker struct {
	baseline map[string]any
	current  map[string]any
	failures []string
}

type parameterEntry struct {
	value    any
	location string
	key      string
}

func (c *compatibilityChecker) comparePaths() {
	baselinePaths := objectAt(c.baseline, "paths")
	currentPaths := objectAt(c.current, "paths")
	for path, baselineValue := range baselinePaths {
		if !strings.HasPrefix(path, "/api/v1/") {
			continue
		}
		pathLocation := jsonPath("$", "paths", path)
		currentValue, ok := currentPaths[path]
		if !ok {
			c.missing(pathLocation)
			continue
		}
		baselineItem, baselineOK := baselineValue.(map[string]any)
		currentItem, currentOK := currentValue.(map[string]any)
		if !baselineOK || !currentOK {
			c.changed(pathLocation, baselineValue, currentValue)
			continue
		}
		for _, method := range []string{"get", "put", "post", "delete", "options", "head", "patch", "trace"} {
			baselineOperation, exists := baselineItem[method]
			if !exists {
				continue
			}
			operationLocation := jsonPath(pathLocation, method)
			currentOperation, exists := currentItem[method]
			if !exists {
				c.missing(operationLocation)
				continue
			}
			c.compareOperation(baselineItem, currentItem, baselineOperation, currentOperation,
				pathLocation, operationLocation)
		}
	}
}

func (c *compatibilityChecker) compareOperation(baselineItem, currentItem map[string]any,
	baselineValue, currentValue any, pathLocation, location string) {
	baseline, baselineOK := baselineValue.(map[string]any)
	current, currentOK := currentValue.(map[string]any)
	if !baselineOK || !currentOK {
		c.changed(location, baselineValue, currentValue)
		return
	}
	c.compareExact(baseline["security"], current["security"], jsonPath(location, "security"))
	c.compareParameters(
		c.effectiveParameters(c.baseline, baselineItem, baseline, pathLocation, location),
		c.effectiveParameters(c.current, currentItem, current, pathLocation, location),
	)
	baselineResponses := objectAt(baseline, "responses")
	currentResponses := objectAt(current, "responses")
	for status, baselineResponse := range baselineResponses {
		responseLocation := jsonPath(location, "responses", status)
		currentResponse, ok := currentResponses[status]
		if !ok {
			c.missing(responseLocation)
			continue
		}
		c.compareResponse(baselineResponse, currentResponse, responseLocation)
	}
}

func (c *compatibilityChecker) effectiveParameters(document map[string]any, pathItem,
	operation map[string]any, pathLocation, operationLocation string) []parameterEntry {
	entries := make([]parameterEntry, 0)
	indexes := make(map[string]int)
	appendEntries := func(values []any, location string) {
		for index, value := range values {
			entry := parameterEntry{value: value, location: jsonIndex(location, index)}
			parameter, ok := c.resolveObject(document, value)
			if ok {
				name, _ := parameter["name"].(string)
				in, _ := parameter["in"].(string)
				entry.key = name + "\x00" + in
			}
			if existing, ok := indexes[entry.key]; ok && entry.key != "\x00" {
				entries[existing] = entry
				continue
			}
			if entry.key == "\x00" {
				entry.key = "\x00" + entry.location
			}
			indexes[entry.key] = len(entries)
			entries = append(entries, entry)
		}
	}
	appendEntries(arrayAt(pathItem, "parameters"), jsonPath(pathLocation, "parameters"))
	appendEntries(arrayAt(operation, "parameters"), jsonPath(operationLocation, "parameters"))
	return entries
}

func (c *compatibilityChecker) compareParameters(baseline, current []parameterEntry) {
	currentByKey := make(map[string]parameterEntry, len(current))
	for _, entry := range current {
		currentByKey[entry.key] = entry
	}
	matched := make(map[string]bool)
	for _, baselineEntry := range baseline {
		baselineParameter, ok := c.resolveObject(c.baseline, baselineEntry.value)
		if !ok {
			c.changed(baselineEntry.location, baselineEntry.value, nil)
			continue
		}
		currentEntry, ok := currentByKey[baselineEntry.key]
		if !ok {
			c.missing(baselineEntry.location)
			continue
		}
		matched[currentEntry.key] = true
		currentParameter, _ := c.resolveObject(c.current, currentEntry.value)
		parameterLocation := currentEntry.location
		c.compareExact(baselineParameter["required"], currentParameter["required"], jsonPath(parameterLocation, "required"))
		c.compareSchema(baselineParameter["schema"], currentParameter["schema"], jsonPath(parameterLocation, "schema"))
	}
	for _, entry := range current {
		if matched[entry.key] {
			continue
		}
		parameter, ok := c.resolveObject(c.current, entry.value)
		if ok && parameter["required"] == true {
			c.failures = append(c.failures, fmt.Sprintf("%s adds a required request parameter", entry.location))
		}
	}
}

func (c *compatibilityChecker) compareResponse(baselineValue, currentValue any, location string) {
	if baselineRef, ok := reference(baselineValue); ok {
		currentRef, currentOK := reference(currentValue)
		if !currentOK || currentRef != baselineRef {
			c.changed(jsonPath(location, "$ref"), baselineRef, currentRef)
		}
		return
	}
	baseline, baselineOK := baselineValue.(map[string]any)
	current, currentOK := currentValue.(map[string]any)
	if !baselineOK || !currentOK {
		c.changed(location, baselineValue, currentValue)
		return
	}
	baselineContent := objectAt(baseline, "content")
	currentContent := objectAt(current, "content")
	for mediaType, baselineMedia := range baselineContent {
		mediaLocation := jsonPath(location, "content", mediaType)
		currentMedia, ok := currentContent[mediaType]
		if !ok {
			c.missing(mediaLocation)
			continue
		}
		baselineMediaObject, baselineOK := baselineMedia.(map[string]any)
		currentMediaObject, currentOK := currentMedia.(map[string]any)
		if !baselineOK || !currentOK {
			c.changed(mediaLocation, baselineMedia, currentMedia)
			continue
		}
		c.compareSchema(baselineMediaObject["schema"], currentMediaObject["schema"], jsonPath(mediaLocation, "schema"))
	}
	baselineHeaders := objectAt(baseline, "headers")
	currentHeaders := objectAt(current, "headers")
	for name, baselineHeader := range baselineHeaders {
		headerLocation := jsonPath(location, "headers", name)
		currentHeader, ok := currentHeaders[name]
		if !ok {
			c.missing(headerLocation)
			continue
		}
		baselineHeaderObject, baselineOK := baselineHeader.(map[string]any)
		currentHeaderObject, currentOK := currentHeader.(map[string]any)
		if !baselineOK || !currentOK {
			c.changed(headerLocation, baselineHeader, currentHeader)
			continue
		}
		c.compareSchema(baselineHeaderObject["schema"], currentHeaderObject["schema"], jsonPath(headerLocation, "schema"))
	}
}

func (c *compatibilityChecker) compareComponents() {
	baselineComponents := objectAt(c.baseline, "components")
	currentComponents := objectAt(c.current, "components")
	for _, section := range []string{"schemas", "parameters", "responses", "securitySchemes"} {
		baselineSection := objectAt(baselineComponents, section)
		currentSection := objectAt(currentComponents, section)
		for name, baselineValue := range baselineSection {
			location := jsonPath("$", "components", section, name)
			currentValue, ok := currentSection[name]
			if !ok {
				c.missing(location)
				continue
			}
			switch section {
			case "schemas":
				c.compareSchema(baselineValue, currentValue, location)
			case "parameters":
				c.compareComponentParameter(baselineValue, currentValue, location)
			case "responses":
				c.compareResponse(baselineValue, currentValue, location)
			case "securitySchemes":
				c.compareSecurityScheme(baselineValue, currentValue, location)
			}
		}
	}
}

func (c *compatibilityChecker) compareComponentParameter(baselineValue, currentValue any, location string) {
	baseline, baselineOK := baselineValue.(map[string]any)
	current, currentOK := currentValue.(map[string]any)
	if !baselineOK || !currentOK {
		c.changed(location, baselineValue, currentValue)
		return
	}
	for _, key := range []string{"name", "in", "required"} {
		c.compareExact(baseline[key], current[key], jsonPath(location, key))
	}
	c.compareSchema(baseline["schema"], current["schema"], jsonPath(location, "schema"))
}

func (c *compatibilityChecker) compareSecurityScheme(baselineValue, currentValue any, location string) {
	baseline, baselineOK := baselineValue.(map[string]any)
	current, currentOK := currentValue.(map[string]any)
	if !baselineOK || !currentOK {
		c.changed(location, baselineValue, currentValue)
		return
	}
	for _, key := range []string{"type", "scheme", "bearerFormat", "in", "name"} {
		if baselineValue, exists := baseline[key]; exists {
			currentValue, currentExists := current[key]
			if !currentExists || !reflect.DeepEqual(baselineValue, currentValue) {
				c.changed(jsonPath(location, key), baselineValue, currentValue)
			}
		}
	}
}

func (c *compatibilityChecker) compareSchema(baselineValue, currentValue any, location string) {
	if baselineValue == nil {
		return
	}
	if baselineRef, ok := reference(baselineValue); ok {
		currentRef, currentOK := reference(currentValue)
		if !currentOK || currentRef != baselineRef {
			c.changed(jsonPath(location, "$ref"), baselineRef, currentRef)
			return
		}
		baselineSiblings := withoutKey(baselineValue, "$ref")
		currentSiblings := withoutKey(currentValue, "$ref")
		c.compareSchema(baselineSiblings, currentSiblings, location)
		baselineSiblingProperties := objectAt(baselineSiblings, "properties")
		referencedProperties := schemaPropertyNames(c.baseline, baselineValue, make(map[string]bool))
		for name := range objectAt(currentSiblings, "properties") {
			if _, alreadyCompared := baselineSiblingProperties[name]; alreadyCompared {
				continue
			}
			if referencedProperties[name] {
				propertyLocation := jsonPath(location, "properties", name)
				c.failures = append(c.failures, propertyLocation+" adds constraints to a referenced property")
			}
		}
		baselineSiblingRequired := stringSlice(baselineSiblings["required"])
		referencedRequired := schemaRequiredNames(c.baseline, baselineValue, make(map[string]bool))
		for _, name := range stringSlice(currentSiblings["required"]) {
			if slices.Contains(baselineSiblingRequired, name) || referencedRequired[name] {
				continue
			}
			if referencedProperties[name] {
				requiredLocation := jsonPath(location, "required", name)
				c.failures = append(c.failures, requiredLocation+" makes a referenced field required")
			}
		}
		return
	}
	baseline, baselineOK := baselineValue.(map[string]any)
	current, currentOK := currentValue.(map[string]any)
	if !baselineOK || !currentOK {
		c.changed(location, baselineValue, currentValue)
		return
	}
	for _, key := range schemaConstraintKeywords {
		baselineConstraint, baselineExists := baseline[key]
		currentConstraint, currentExists := current[key]
		if baselineExists != currentExists || !reflect.DeepEqual(baselineConstraint, currentConstraint) {
			c.changed(jsonPath(location, key), baselineConstraint, currentConstraint)
		}
	}
	if baselineEnum, exists := baseline["enum"]; exists {
		currentEnum, currentExists := current["enum"]
		if !currentExists || !equalJSONSet(baselineEnum, currentEnum) {
			c.changed(jsonPath(location, "enum"), baselineEnum, currentEnum)
		}
	} else if _, exists := current["enum"]; exists {
		c.changed(jsonPath(location, "enum"), nil, current["enum"])
	}
	baselineRequired := stringSlice(baseline["required"])
	currentRequired := stringSlice(current["required"])
	for _, name := range baselineRequired {
		if !slices.Contains(currentRequired, name) {
			c.missing(jsonPath(location, "required", name))
		}
	}
	baselineProperties := objectAt(baseline, "properties")
	currentProperties := objectAt(current, "properties")
	for _, name := range currentRequired {
		if slices.Contains(baselineRequired, name) {
			continue
		}
		if _, existed := baselineProperties[name]; existed {
			c.failures = append(c.failures, jsonPath(location, "required", name)+" makes an existing field required")
		}
	}
	for name, baselineProperty := range baselineProperties {
		propertyLocation := jsonPath(location, "properties", name)
		currentProperty, ok := currentProperties[name]
		if !ok {
			c.missing(propertyLocation)
			continue
		}
		c.compareSchema(baselineProperty, currentProperty, propertyLocation)
	}
	if baselineItems, exists := baseline["items"]; exists {
		currentItems, ok := current["items"]
		if !ok {
			c.missing(jsonPath(location, "items"))
		} else {
			c.compareSchema(baselineItems, currentItems, jsonPath(location, "items"))
		}
	} else if currentItems, exists := current["items"]; exists {
		c.changed(jsonPath(location, "items"), nil, currentItems)
	}
	for _, keyword := range []string{"allOf", "oneOf", "anyOf"} {
		baselineItems := arrayAt(baseline, keyword)
		currentItems := arrayAt(current, keyword)
		for index, baselineItem := range baselineItems {
			itemLocation := jsonIndex(jsonPath(location, keyword), index)
			if index >= len(currentItems) {
				c.missing(itemLocation)
				continue
			}
			c.compareSchema(baselineItem, currentItems[index], itemLocation)
		}
		for index := len(baselineItems); index < len(currentItems); index++ {
			itemLocation := jsonIndex(jsonPath(location, keyword), index)
			c.failures = append(c.failures, itemLocation+" adds a schema composition member")
		}
	}
	if baselineAdditional, exists := baseline["additionalProperties"]; exists {
		currentAdditional, ok := current["additionalProperties"]
		if !ok {
			c.missing(jsonPath(location, "additionalProperties"))
		} else if _, isSchema := baselineAdditional.(map[string]any); isSchema {
			c.compareSchema(baselineAdditional, currentAdditional, jsonPath(location, "additionalProperties"))
		} else {
			c.compareExact(baselineAdditional, currentAdditional, jsonPath(location, "additionalProperties"))
		}
	} else if currentAdditional, exists := current["additionalProperties"]; exists {
		c.changed(jsonPath(location, "additionalProperties"), nil, currentAdditional)
	}
}

func (c *compatibilityChecker) resolveObject(document map[string]any, value any) (map[string]any, bool) {
	if referenceValue, ok := reference(value); ok {
		resolved, ok := resolveJSONPointer(document, referenceValue)
		if !ok {
			return nil, false
		}
		value = resolved
	}
	object, ok := value.(map[string]any)
	return object, ok
}

func (c *compatibilityChecker) compareExact(baseline, current any, location string) {
	if !reflect.DeepEqual(baseline, current) {
		c.changed(location, baseline, current)
	}
}

func (c *compatibilityChecker) missing(location string) {
	c.failures = append(c.failures, location+" is missing")
}

func (c *compatibilityChecker) changed(location string, baseline, current any) {
	c.failures = append(c.failures, fmt.Sprintf("%s changed from %s to %s", location,
		compactJSON(baseline), compactJSON(current)))
}

func resolveJSONPointer(document map[string]any, pointer string) (any, bool) {
	if !strings.HasPrefix(pointer, "#/") {
		return nil, false
	}
	var current any = document
	for _, encoded := range strings.Split(strings.TrimPrefix(pointer, "#/"), "/") {
		key := strings.ReplaceAll(strings.ReplaceAll(encoded, "~1", "/"), "~0", "~")
		object, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = object[key]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func reference(value any) (string, bool) {
	object, ok := value.(map[string]any)
	if !ok {
		return "", false
	}
	referenceValue, ok := object["$ref"].(string)
	return referenceValue, ok
}

func withoutKey(value any, key string) map[string]any {
	object, _ := value.(map[string]any)
	result := make(map[string]any, len(object))
	for name, item := range object {
		if name != key {
			result[name] = item
		}
	}
	return result
}

func schemaPropertyNames(document map[string]any, value any, visited map[string]bool) map[string]bool {
	result := make(map[string]bool)
	if pointer, ok := reference(value); ok {
		if visited[pointer] {
			return result
		}
		visited[pointer] = true
		resolved, ok := resolveJSONPointer(document, pointer)
		if !ok {
			return result
		}
		for name := range schemaPropertyNames(document, resolved, visited) {
			result[name] = true
		}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return result
	}
	for name := range objectAt(object, "properties") {
		result[name] = true
	}
	for _, keyword := range []string{"allOf", "oneOf", "anyOf"} {
		for _, item := range arrayAt(object, keyword) {
			for name := range schemaPropertyNames(document, item, visited) {
				result[name] = true
			}
		}
	}
	return result
}

func schemaRequiredNames(document map[string]any, value any, visited map[string]bool) map[string]bool {
	result := make(map[string]bool)
	if pointer, ok := reference(value); ok {
		if visited[pointer] {
			return result
		}
		visited[pointer] = true
		resolved, ok := resolveJSONPointer(document, pointer)
		if !ok {
			return result
		}
		for name := range schemaRequiredNames(document, resolved, visited) {
			result[name] = true
		}
	}
	object, ok := value.(map[string]any)
	if !ok {
		return result
	}
	for _, name := range stringSlice(object["required"]) {
		result[name] = true
	}
	for _, item := range arrayAt(object, "allOf") {
		for name := range schemaRequiredNames(document, item, visited) {
			result[name] = true
		}
	}
	return result
}

func objectAt(object map[string]any, key string) map[string]any {
	value, _ := object[key].(map[string]any)
	if value == nil {
		return map[string]any{}
	}
	return value
}

func arrayAt(object map[string]any, key string) []any {
	value, _ := object[key].([]any)
	return value
}

func jsonPath(base string, keys ...string) string {
	for _, key := range keys {
		base += "[" + strconv.Quote(key) + "]"
	}
	return base
}

func jsonIndex(base string, index int) string {
	return base + "[" + strconv.Itoa(index) + "]"
}

func compactJSON(value any) string {
	contents, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%#v", value)
	}
	return string(contents)
}

func equalJSONSet(left, right any) bool {
	leftValues, leftOK := left.([]any)
	rightValues, rightOK := right.([]any)
	if !leftOK || !rightOK || len(leftValues) != len(rightValues) {
		return false
	}
	leftEncoded := make([]string, 0, len(leftValues))
	rightEncoded := make([]string, 0, len(rightValues))
	for _, value := range leftValues {
		leftEncoded = append(leftEncoded, compactJSON(value))
	}
	for _, value := range rightValues {
		rightEncoded = append(rightEncoded, compactJSON(value))
	}
	slices.Sort(leftEncoded)
	slices.Sort(rightEncoded)
	return slices.Equal(leftEncoded, rightEncoded)
}

func cloneDocument(t *testing.T, document map[string]any) map[string]any {
	t.Helper()
	contents, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	var clone map[string]any
	if err := json.Unmarshal(contents, &clone); err != nil {
		t.Fatal(err)
	}
	return clone
}

func schemaProperties(document map[string]any, name string) map[string]any {
	return document["components"].(map[string]any)["schemas"].(map[string]any)[name].(map[string]any)["properties"].(map[string]any)
}

func weatherOperation(document map[string]any, path string) map[string]any {
	return weatherPathItem(document, path)["get"].(map[string]any)
}

func weatherPathItem(document map[string]any, path string) map[string]any {
	return document["paths"].(map[string]any)[path].(map[string]any)
}

func responseSchema(document map[string]any, path, status string) map[string]any {
	return weatherOperation(document, path)["responses"].(map[string]any)[status].(map[string]any)["content"].(map[string]any)["application/json"].(map[string]any)["schema"].(map[string]any)
}
