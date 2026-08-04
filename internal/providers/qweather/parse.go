package qweather

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/MingYuan0415/mt-server/internal/modules/weather"
)

func parseCurrent(body []byte) (weather.ProviderResult, error) {
	var raw currentResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return weather.ProviderResult{}, fmt.Errorf("decode current weather: %w", err)
	}
	updatedAt, err := parseTime(raw.UpdateTime, "updateTime")
	if err != nil {
		return weather.ProviderResult{}, err
	}
	observedAt, err := parseTime(raw.Now.ObservedAt, "now.obsTime")
	if err != nil {
		return weather.ProviderResult{}, err
	}
	current := weather.Current{
		ObservedAt:    observedAt,
		ConditionCode: raw.Now.Icon,
		ConditionText: raw.Now.Text,
		WindDirection: raw.Now.WindDirection,
		WindScale:     raw.Now.WindScale,
	}
	if current.TemperatureC, err = requiredNumber(raw.Now.Temperature, "now.temp"); err != nil {
		return weather.ProviderResult{}, err
	}
	if current.FeelsLikeC, err = requiredNumber(raw.Now.FeelsLike, "now.feelsLike"); err != nil {
		return weather.ProviderResult{}, err
	}
	if current.WindDegrees, err = requiredNumber(raw.Now.WindDegrees, "now.wind360"); err != nil {
		return weather.ProviderResult{}, err
	}
	if current.WindSpeedKMH, err = requiredNumber(raw.Now.WindSpeed, "now.windSpeed"); err != nil {
		return weather.ProviderResult{}, err
	}
	if current.HumidityPercent, err = requiredNumber(raw.Now.Humidity, "now.humidity"); err != nil {
		return weather.ProviderResult{}, err
	}
	if current.PrecipitationMM, err = requiredNumber(raw.Now.Precipitation, "now.precip"); err != nil {
		return weather.ProviderResult{}, err
	}
	if current.PressureHPA, err = requiredNumber(raw.Now.Pressure, "now.pressure"); err != nil {
		return weather.ProviderResult{}, err
	}
	if current.VisibilityKM, err = requiredNumber(raw.Now.Visibility, "now.vis"); err != nil {
		return weather.ProviderResult{}, err
	}
	if current.CloudPercent, err = optionalNumber(raw.Now.Cloud, "now.cloud"); err != nil {
		return weather.ProviderResult{}, err
	}
	if current.DewPointC, err = optionalNumber(raw.Now.DewPoint, "now.dew"); err != nil {
		return weather.ProviderResult{}, err
	}
	if current.ConditionCode == "" || current.ConditionText == "" {
		return weather.ProviderResult{}, errors.New("current weather condition is missing")
	}
	return weather.ProviderResult{UpdatedAt: updatedAt, Data: current}, nil
}

func parseHourly(body []byte) (weather.ProviderResult, error) {
	var raw hourlyResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return weather.ProviderResult{}, fmt.Errorf("decode hourly weather: %w", err)
	}
	updatedAt, err := parseTime(raw.UpdateTime, "updateTime")
	if err != nil {
		return weather.ProviderResult{}, err
	}
	if len(raw.Hourly) == 0 {
		return weather.ProviderResult{}, errors.New("hourly forecast is empty")
	}
	limit := len(raw.Hourly)
	if limit > 24 {
		limit = 24
	}
	hours := make([]weather.Hour, 0, limit)
	for index := 0; index < limit; index++ {
		item := raw.Hourly[index]
		hour := weather.Hour{
			ConditionCode: item.Icon,
			ConditionText: item.Text,
			WindDirection: item.WindDirection,
			WindScale:     item.WindScale,
		}
		field := func(name string) string { return fmt.Sprintf("hourly[%d].%s", index, name) }
		if hour.ForecastAt, err = parseTime(item.ForecastAt, field("fxTime")); err != nil {
			return weather.ProviderResult{}, err
		}
		if hour.TemperatureC, err = requiredNumber(item.Temperature, field("temp")); err != nil {
			return weather.ProviderResult{}, err
		}
		if hour.WindDegrees, err = requiredNumber(item.WindDegrees, field("wind360")); err != nil {
			return weather.ProviderResult{}, err
		}
		if hour.WindSpeedKMH, err = requiredNumber(item.WindSpeed, field("windSpeed")); err != nil {
			return weather.ProviderResult{}, err
		}
		if hour.HumidityPercent, err = requiredNumber(item.Humidity, field("humidity")); err != nil {
			return weather.ProviderResult{}, err
		}
		if hour.PrecipitationChance, err = requiredNumber(item.PrecipitationChance, field("pop")); err != nil {
			return weather.ProviderResult{}, err
		}
		if hour.PrecipitationMM, err = requiredNumber(item.Precipitation, field("precip")); err != nil {
			return weather.ProviderResult{}, err
		}
		if hour.PressureHPA, err = requiredNumber(item.Pressure, field("pressure")); err != nil {
			return weather.ProviderResult{}, err
		}
		if hour.CloudPercent, err = optionalNumber(item.Cloud, field("cloud")); err != nil {
			return weather.ProviderResult{}, err
		}
		if hour.DewPointC, err = optionalNumber(item.DewPoint, field("dew")); err != nil {
			return weather.ProviderResult{}, err
		}
		if hour.ConditionCode == "" || hour.ConditionText == "" {
			return weather.ProviderResult{}, fmt.Errorf("%s: condition is missing", field("condition"))
		}
		hours = append(hours, hour)
	}
	return weather.ProviderResult{UpdatedAt: updatedAt, Data: weather.Hourly{Hours: hours}}, nil
}

func parseDaily(body []byte) (weather.ProviderResult, error) {
	var raw dailyResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return weather.ProviderResult{}, fmt.Errorf("decode daily weather: %w", err)
	}
	updatedAt, err := parseTime(raw.UpdateTime, "updateTime")
	if err != nil {
		return weather.ProviderResult{}, err
	}
	if len(raw.Daily) == 0 {
		return weather.ProviderResult{}, errors.New("daily forecast is empty")
	}
	limit := len(raw.Daily)
	if limit > 7 {
		limit = 7
	}
	days := make([]weather.Day, 0, limit)
	for index := 0; index < limit; index++ {
		item := raw.Daily[index]
		field := func(name string) string { return fmt.Sprintf("daily[%d].%s", index, name) }
		if _, err := time.Parse("2006-01-02", item.Date); err != nil {
			return weather.ProviderResult{}, fmt.Errorf("%s: invalid date", field("fxDate"))
		}
		day := weather.Day{
			Date:               item.Date,
			MoonPhase:          item.MoonPhase,
			MoonPhaseCode:      item.MoonPhaseCode,
			ConditionDayCode:   item.IconDay,
			ConditionDayText:   item.TextDay,
			ConditionNightCode: item.IconNight,
			ConditionNightText: item.TextNight,
			WindDayDirection:   item.WindDayDirection,
			WindDayScale:       item.WindDayScale,
			WindNightDirection: item.WindNightDirection,
			WindNightScale:     item.WindNightScale,
		}
		if day.Sunrise, err = parseClock(item.Date, item.Sunrise, updatedAt, field("sunrise")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.Sunset, err = parseClock(item.Date, item.Sunset, updatedAt, field("sunset")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.Moonrise, err = parseClock(item.Date, item.Moonrise, updatedAt, field("moonrise")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.Moonset, err = parseClock(item.Date, item.Moonset, updatedAt, field("moonset")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.TemperatureMaxC, err = requiredNumber(item.TemperatureMax, field("tempMax")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.TemperatureMinC, err = requiredNumber(item.TemperatureMin, field("tempMin")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.WindDayDegrees, err = requiredNumber(item.WindDayDegrees, field("wind360Day")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.WindDaySpeedKMH, err = requiredNumber(item.WindDaySpeed, field("windSpeedDay")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.WindNightDegrees, err = requiredNumber(item.WindNightDegrees, field("wind360Night")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.WindNightSpeedKMH, err = requiredNumber(item.WindNightSpeed, field("windSpeedNight")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.HumidityPercent, err = requiredNumber(item.Humidity, field("humidity")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.PrecipitationMM, err = requiredNumber(item.Precipitation, field("precip")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.PressureHPA, err = requiredNumber(item.Pressure, field("pressure")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.VisibilityKM, err = requiredNumber(item.Visibility, field("vis")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.CloudPercent, err = optionalNumber(item.Cloud, field("cloud")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.UVIndex, err = requiredNumber(item.UVIndex, field("uvIndex")); err != nil {
			return weather.ProviderResult{}, err
		}
		if day.ConditionDayCode == "" || day.ConditionDayText == "" ||
			day.ConditionNightCode == "" || day.ConditionNightText == "" {
			return weather.ProviderResult{}, fmt.Errorf("%s: condition is missing", field("condition"))
		}
		days = append(days, day)
	}
	return weather.ProviderResult{UpdatedAt: updatedAt, Data: weather.Daily{Days: days}}, nil
}

const (
	maximumAlerts           = 32
	maximumAlertIDRunes     = 128
	maximumAlertTitleRunes  = 256
	maximumAlertFieldRunes  = 128
	maximumDescriptionRunes = 8192
	maximumInstructionRunes = 4096
)

func parseAlerts(body []byte) (weather.ProviderResult, error) {
	var raw alertsResponse
	if err := json.Unmarshal(body, &raw); err != nil {
		return weather.ProviderResult{}, fmt.Errorf("decode weather alerts: %w", err)
	}
	updatedAt, err := parseTime(raw.UpdateTime, "updateTime")
	if err != nil {
		return weather.ProviderResult{}, err
	}
	items := make([]weather.Alert, 0, len(raw.Warning))
	for index, item := range raw.Warning {
		field := func(name string) string { return fmt.Sprintf("warning[%d].%s", index, name) }
		alert := weather.Alert{}
		var truncated bool
		alert.ID, truncated = boundedText(item.ID, maximumAlertIDRunes)
		alert.ContentTruncated = truncated
		alert.Title, truncated = boundedText(item.Title, maximumAlertTitleRunes)
		alert.ContentTruncated = alert.ContentTruncated || truncated
		authority := item.SenderName
		if strings.TrimSpace(authority) == "" {
			authority = item.Sender
		}
		alert.IssuingAuthority, truncated = boundedText(authority, maximumAlertFieldRunes)
		alert.ContentTruncated = alert.ContentTruncated || truncated
		alert.TypeCode, truncated = boundedText(item.TypeCode, maximumAlertFieldRunes)
		alert.ContentTruncated = alert.ContentTruncated || truncated
		alert.TypeName, truncated = boundedText(item.TypeName, maximumAlertFieldRunes)
		alert.ContentTruncated = alert.ContentTruncated || truncated
		alert.Description, truncated = boundedText(item.Text, maximumDescriptionRunes)
		alert.ContentTruncated = alert.ContentTruncated || truncated
		alert.Instruction, truncated = boundedText(item.Instruction, maximumInstructionRunes)
		alert.ContentTruncated = alert.ContentTruncated || truncated
		if alert.ID == "" || alert.Title == "" || alert.TypeCode == "" || alert.TypeName == "" {
			return weather.ProviderResult{}, fmt.Errorf("%s: required identity is missing", field("identity"))
		}
		if alert.IssuedAt, err = parseTime(item.PublishedAt, field("pubTime")); err != nil {
			return weather.ProviderResult{}, err
		}
		alert.IssuedAt = alert.IssuedAt.UTC()
		if alert.StartsAt, err = parseOptionalTime(item.StartsAt, field("startTime")); err != nil {
			return weather.ProviderResult{}, err
		}
		if alert.EndsAt, err = parseOptionalTime(item.EndsAt, field("endTime")); err != nil {
			return weather.ProviderResult{}, err
		}
		alert.Severity = normalizeSeverity(item.Severity, item.Level)
		alert.Status = normalizeStatus(item.Status)
		alert.Urgency = normalizeCAPValue(item.Urgency,
			"future", "expected", "immediate", "past")
		alert.Certainty = normalizeCAPValue(item.Certainty,
			"unlikely", "possible", "likely", "observed")
		items = append(items, alert)
	}
	sort.SliceStable(items, func(left, right int) bool {
		if statusPriority(items[left].Status) != statusPriority(items[right].Status) {
			return statusPriority(items[left].Status) > statusPriority(items[right].Status)
		}
		if severityPriority(items[left].Severity) != severityPriority(items[right].Severity) {
			return severityPriority(items[left].Severity) > severityPriority(items[right].Severity)
		}
		if !items[left].IssuedAt.Equal(items[right].IssuedAt) {
			return items[left].IssuedAt.After(items[right].IssuedAt)
		}
		return items[left].ID < items[right].ID
	})
	truncated := len(items) > maximumAlerts
	if truncated {
		items = items[:maximumAlerts]
	}
	return weather.ProviderResult{UpdatedAt: updatedAt.UTC(), Data: weather.Alerts{
		DetailURL: qweatherDetailURL(raw.FXLink), Truncated: truncated, Items: items,
	}}, nil
}

func parseOptionalTime(value, field string) (*time.Time, error) {
	if strings.TrimSpace(value) == "" {
		return nil, nil
	}
	parsed, err := parseTime(value, field)
	if err != nil {
		return nil, err
	}
	parsed = parsed.UTC()
	return &parsed, nil
}

func boundedText(value string, maximum int) (string, bool) {
	value = strings.TrimSpace(value)
	runes := []rune(value)
	if len(runes) <= maximum {
		return value, false
	}
	return string(runes[:maximum]), true
}

func normalizeSeverity(value, level string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "minor":
		return "minor"
	case "moderate":
		return "moderate"
	case "severe":
		return "severe"
	case "extreme":
		return "extreme"
	}
	switch strings.ToLower(strings.TrimSpace(level)) {
	case "blue", "蓝色", "蓝":
		return "minor"
	case "yellow", "黄色", "黄":
		return "moderate"
	case "orange", "橙色", "橙":
		return "severe"
	case "red", "红色", "红":
		return "extreme"
	default:
		return "unknown"
	}
}

func normalizeStatus(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "active", "预警中", "生效中":
		return "active"
	case "cancelled", "canceled", "取消预警", "预警解除", "已解除":
		return "cancelled"
	default:
		return "unknown"
	}
}

func normalizeCAPValue(value string, allowed ...string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	for _, candidate := range allowed {
		if value == candidate {
			return value
		}
	}
	return "unknown"
}

func statusPriority(status string) int {
	switch status {
	case "active":
		return 2
	case "unknown":
		return 1
	default:
		return 0
	}
}

func severityPriority(severity string) int {
	switch severity {
	case "extreme":
		return 4
	case "severe":
		return 3
	case "moderate":
		return 2
	case "minor":
		return 1
	default:
		return 0
	}
}

func qweatherDetailURL(value string) string {
	parsed, err := url.Parse(strings.TrimSpace(value))
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil {
		return ""
	}
	host := strings.ToLower(parsed.Hostname())
	if host != "qweather.com" && !strings.HasSuffix(host, ".qweather.com") &&
		host != "hfx.link" && !strings.HasSuffix(host, ".hfx.link") {
		return ""
	}
	return parsed.String()
}

func parseTime(value, field string) (time.Time, error) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04Z07:00"} {
		parsed, err := time.Parse(layout, value)
		if err == nil {
			return parsed, nil
		}
	}
	return time.Time{}, fmt.Errorf("%s: invalid timestamp", field)
}

func parseClock(date, value string, reference time.Time, field string) (*time.Time, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	parsed, err := time.ParseInLocation("2006-01-02 15:04", date+" "+value, reference.Location())
	if err != nil {
		return nil, fmt.Errorf("%s: invalid local time", field)
	}
	return &parsed, nil
}

func requiredNumber(value, field string) (float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return 0, fmt.Errorf("%s: value is missing", field)
	}
	number, err := strconv.ParseFloat(value, 64)
	if err != nil || math.IsNaN(number) || math.IsInf(number, 0) {
		return 0, fmt.Errorf("%s: invalid number", field)
	}
	return number, nil
}

func optionalNumber(value, field string) (*float64, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, nil
	}
	number, err := requiredNumber(value, field)
	if err != nil {
		return nil, err
	}
	return &number, nil
}
