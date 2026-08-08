package location

import (
	"strings"
	"sync"
	"testing"
	"time"
)

func TestApplyLocalizedOverlaysValidatedFields(t *testing.T) {
	point := Point{Latitude: 22.5, Longitude: 114.1, City: "Shenzhen",
		Region: "Guangdong", Country: "CN", Timezone: "Asia/Shanghai",
		Source: "ip", Provider: "maxmind", Precision: "coarse", Key: "abc"}
	updated, changed := ApplyLocalized(point, LocalizedMetadata{
		City: " 深圳市 ", District: "南山区", Region: "广东省", Timezone: "Asia/Shanghai",
	})
	if updated.City != "深圳市" || updated.District != "南山区" || updated.Region != "广东省" ||
		!changed {
		t.Fatalf("localized fields were not overlaid: %#v", updated)
	}
	if updated.Country != "CN" || updated.Latitude != 22.5 || updated.Longitude != 114.1 ||
		updated.Source != "ip" || updated.Provider != "maxmind" || updated.Precision != "coarse" ||
		updated.Key != "abc" {
		t.Fatalf("localization must not alter provenance or coordinates: %#v", updated)
	}
	if point.City != "Shenzhen" || point.District != "" {
		t.Fatalf("ApplyLocalized must not mutate its input: %#v", point)
	}
}

func TestApplyLocalizedSkipsInvalidAndEmptyFields(t *testing.T) {
	point := Point{City: "Shenzhen", District: "Nanshan",
		Region: "Guangdong", Timezone: "Asia/Shanghai"}
	updated, changed := ApplyLocalized(point, LocalizedMetadata{
		City: " ", District: strings.Repeat("x", 129), Region: "bad\tzone", Timezone: "",
	})
	if updated.City != "Shenzhen" || updated.District != "Nanshan" ||
		updated.Region != "Guangdong" || updated.Timezone != "Asia/Shanghai" || changed {
		t.Fatalf("invalid localized values must be skipped: %#v", updated)
	}
}

func TestApplyLocalizedReportsNoChangeForIdenticalValues(t *testing.T) {
	point := Point{City: "深圳市", District: "南山区",
		Region: "广东省", Timezone: "Asia/Shanghai"}
	_, changed := ApplyLocalized(point, LocalizedMetadata{
		City: "深圳市", District: "南山区", Region: "广东省", Timezone: "Asia/Shanghai",
	})
	if changed {
		t.Fatal("identical values must not report a change")
	}
	_, changed = ApplyLocalized(point, LocalizedMetadata{})
	if changed {
		t.Fatal("empty metadata must not report a change")
	}
	districtOnly := Point{City: "深圳市"}
	updated, changed := ApplyLocalized(districtOnly, LocalizedMetadata{District: "南山区"})
	if updated.District != "南山区" || !changed || updated.City != "深圳市" {
		t.Fatalf("district-only overlay must report a change: %#v", updated)
	}
}

func TestLocalizeLimiterEnforcesPerDeviceBudgetAndRefill(t *testing.T) {
	limiter := NewLocalizeLimiter(2, time.Minute, 4)
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }

	first, ok := limiter.Try("device_a")
	if !ok {
		t.Fatal("first budgeted attempt was rejected")
	}
	first()
	second, ok := limiter.Try("device_a")
	if !ok {
		t.Fatal("second budgeted attempt was rejected")
	}
	second()
	if release, ok := limiter.Try("device_a"); ok {
		release()
		t.Fatal("exhausted device budget was accepted")
	}
	if release, ok := limiter.Try("device_b"); !ok {
		t.Fatal("another device must have an independent budget")
	} else {
		release()
	}

	now = now.Add(time.Minute)
	if release, ok := limiter.Try("device_a"); !ok {
		t.Fatal("refilled device budget was rejected")
	} else {
		release()
	}
}

func TestLocalizeLimiterCapsGlobalConcurrency(t *testing.T) {
	limiter := NewLocalizeLimiter(100, time.Minute, 2)
	first, ok := limiter.Try("device_a")
	if !ok {
		t.Fatal("first in-flight slot was rejected")
	}
	second, ok := limiter.Try("device_b")
	if !ok {
		t.Fatal("second in-flight slot was rejected")
	}
	if release, ok := limiter.Try("device_c"); ok {
		release()
		t.Fatal("concurrency cap was exceeded")
	}
	first()
	release, ok := limiter.Try("device_c")
	if !ok {
		t.Fatal("slot was not released")
	}
	release()
	second()
}

func TestLocalizeLimiterReleaseIsIdempotent(t *testing.T) {
	limiter := NewLocalizeLimiter(100, time.Minute, 1)
	release, ok := limiter.Try("device_a")
	if !ok {
		t.Fatal("in-flight slot was rejected")
	}
	release()
	release()
	if other, ok := limiter.Try("device_b"); !ok {
		t.Fatal("idempotent release did not free the slot")
	} else {
		other()
	}
}

func TestLocalizeLimiterRetainClearsRevokedDevices(t *testing.T) {
	limiter := NewLocalizeLimiter(1, time.Minute, 4)
	release, ok := limiter.Try("device_a")
	if !ok {
		t.Fatal("budgeted attempt was rejected")
	}
	release()
	limiter.Retain([]string{"device_b"})
	if release, ok := limiter.Try("device_a"); !ok {
		t.Fatal("revoked device budget must be reset")
	} else {
		release()
	}
}

func TestLocalizeLimiterSlotRejectionDoesNotConsumeBudget(t *testing.T) {
	limiter := NewLocalizeLimiter(1, time.Minute, 1)
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }

	first, ok := limiter.Try("device_a")
	if !ok {
		t.Fatal("first in-flight slot was rejected")
	}
	if release, ok := limiter.Try("device_b"); ok {
		release()
		t.Fatal("concurrency cap must reject a second device")
	}
	first()
	if release, ok := limiter.Try("device_b"); !ok {
		t.Fatal("slot rejection must not consume the device budget")
	} else {
		release()
	}
}

func TestLocalizeLimiterBudgetRejectionRefundsSlot(t *testing.T) {
	limiter := NewLocalizeLimiter(1, time.Minute, 1)
	now := time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC)
	limiter.now = func() time.Time { return now }

	release, ok := limiter.Try("device_a")
	if !ok {
		t.Fatal("first budgeted attempt was rejected")
	}
	release()
	if first, ok := limiter.Try("device_a"); ok {
		first()
		t.Fatal("exhausted device budget was accepted")
	}
	if release, ok := limiter.Try("device_b"); !ok {
		t.Fatal("budget rejection must refund the in-flight slot")
	} else {
		release()
	}
}

func TestLocalizeLimiterRejectsInvalidConfiguration(t *testing.T) {
	for _, args := range []struct {
		capacity    int
		interval    time.Duration
		concurrency int
	}{
		{0, time.Minute, 1},
		{1, 0, 1},
		{1, -time.Second, 1},
		{1, time.Minute, 0},
	} {
		func() {
			defer func() {
				if recover() == nil {
					t.Fatalf("invalid configuration %#v did not panic", args)
				}
			}()
			NewLocalizeLimiter(args.capacity, args.interval, args.concurrency)
		}()
	}
}

func TestLocalizeLimiterConcurrentUse(t *testing.T) {
	limiter := NewLocalizeLimiter(4, time.Second, 4)
	var group sync.WaitGroup
	for range 8 {
		group.Add(1)
		go func() {
			defer group.Done()
			if release, ok := limiter.Try("device_a"); ok {
				release()
			}
		}()
	}
	group.Wait()
}
