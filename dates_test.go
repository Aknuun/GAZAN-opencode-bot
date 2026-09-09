package main

import (
	"testing"
	"time"
)

func TestPersianTimeLabel(t *testing.T) {
	// 2026-09-09 09:00 UTC = ۱۲:۳۰ به وقت ایران = ۱۸ شهریور ۱۴۰۵
	ms := time.Date(2026, 9, 9, 9, 0, 0, 0, time.UTC).UnixMilli()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	got := persianTimeLabel(ms, now)
	want := "۱۸ شهریور ۱۲:۳۰"
	if got != want {
		t.Fatalf("got %q want %q", got, want)
	}

	// سال متفاوت → سال شمسی هم اضافه شود
	prev := time.Date(2025, 3, 1, 0, 0, 0, 0, time.UTC).UnixMilli() // اسفند ۱۴۰۳
	got2 := persianTimeLabel(prev, now)
	if got2 != "۱۱ اسفند ۱۴۰۳ ۰۳:۳۰" {
		t.Fatalf("got %q", got2)
	}
}
