package clock_test

import (
	"sync"
	"testing"
	"time"

	"github.com/vance1852/orientation-enrollment-platform/internal/platform/clock"
)

func TestSystemClockReportsUTC(t *testing.T) {
	var source clock.Clock = clock.System{}
	now := source.Now()
	if now.Location() != time.UTC {
		t.Fatalf("Now() location = %s, want UTC", now.Location())
	}
	if time.Since(now) > time.Minute {
		t.Fatalf("Now() = %s, which is not close to the wall clock", now)
	}
	shanghai := clock.BusinessLocation("Asia/Shanghai")
	if got := source.In(shanghai); got.Location() != shanghai {
		t.Fatalf("In() location = %s", got.Location())
	}
	if got := source.In(nil); got.Location() != time.UTC {
		t.Fatalf("In(nil) must fall back to UTC, got %s", got.Location())
	}
}

func TestBusinessLocationFallsBackToTheCampusOffset(t *testing.T) {
	loc := clock.BusinessLocation("Not/AZone")
	reference := time.Date(2026, time.August, 26, 12, 0, 0, 0, loc)
	_, offset := reference.Zone()
	if offset != 8*60*60 {
		t.Fatalf("fallback offset = %d seconds, want 28800", offset)
	}
}

func TestFixedClockIsDeterministicAndAdvancesOnDemand(t *testing.T) {
	anchor := time.Date(2026, time.August, 26, 10, 0, 0, 0, time.UTC)
	fixed := clock.NewFixed(anchor)

	if !fixed.Now().Equal(anchor) {
		t.Fatalf("Now() = %s, want %s", fixed.Now(), anchor)
	}
	if !fixed.Now().Equal(fixed.Now()) {
		t.Fatal("a fixed clock must not move on its own")
	}
	if got := fixed.Advance(90 * time.Minute); !got.Equal(anchor.Add(90 * time.Minute)) {
		t.Fatalf("Advance() = %s", got)
	}
	fixed.Set(anchor)
	if !fixed.Now().Equal(anchor) {
		t.Fatalf("Set() did not reset the clock, got %s", fixed.Now())
	}
	shanghai := clock.BusinessLocation("Asia/Shanghai")
	if got := fixed.In(shanghai); got.Hour() != 18 {
		t.Fatalf("10:00 UTC must render as 18:00 campus time, got %s", got)
	}
	if got := fixed.In(nil); !got.Equal(anchor) {
		t.Fatalf("In(nil) = %s", got)
	}
}

func TestFixedClockIsSafeUnderConcurrentUse(t *testing.T) {
	fixed := clock.NewFixed(time.Date(2026, time.August, 26, 10, 0, 0, 0, time.UTC))
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			fixed.Advance(time.Second)
		}()
		go func() {
			defer wg.Done()
			_ = fixed.Now()
		}()
	}
	wg.Wait()
	if fixed.Now().Second() != 16 {
		t.Fatalf("after 16 advances the clock reads %s", fixed.Now())
	}
}
