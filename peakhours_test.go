package main

import (
	"path/filepath"
	"testing"
	"time"

	"opencode-tg-bot/internal/occlient"
)

func TestDeepseekPeakWindows(t *testing.T) {
	p := newPeakStore(filepath.Join(t.TempDir(), "peakhours.json"))
	if err := p.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	t.Cleanup(func() { p.close() })

	if !p.enabledFor("deepseek") {
		t.Fatalf("deepseek reminders should be enabled by default")
	}

	// دوشنبه ۰۵:۰۰ تهران => ۰۱:۳۰ UTC => داخل پیک ۰۱:۰۰–۰۴:۰۰؛ پایان ۰۷:۳۰
	mon0500 := time.Date(2026, time.September, 14, 5, 0, 0, 0, tehranLoc)
	if end, ok := p.peakEnd("deepseek", mon0500); !ok || end != 7*60+30 {
		t.Fatalf("mon 05:00: end=%d ok=%v; want %d", end, ok, 7*60+30)
	}

	// دوشنبه ۰۸:۰۰ تهران => ۰۴:۳۰ UTC => بین دو پنجرهٔ پیک
	mon0800 := time.Date(2026, time.September, 14, 8, 0, 0, 0, tehranLoc)
	if _, ok := p.peakEnd("deepseek", mon0800); ok {
		t.Fatalf("mon 08:00 should be off-peak")
	}

	// دوشنبه ۱۰:۰۰ تهران => ۰۶:۳۰ UTC => پنجرهٔ دوم؛ پایان ۱۳:۳۰
	mon1000 := time.Date(2026, time.September, 14, 10, 0, 0, 0, tehranLoc)
	if end, ok := p.peakEnd("deepseek", mon1000); !ok || end != 13*60+30 {
		t.Fatalf("mon 10:00: end=%d ok=%v; want %d", end, ok, 13*60+30)
	}

	// شنبه ۰۵:۰۰ تهران => آخر هفته؛ پیک نیست
	sat0500 := time.Date(2026, time.September, 12, 5, 0, 0, 0, tehranLoc)
	if _, ok := p.peakEnd("deepseek", sat0500); ok {
		t.Fatalf("saturday should be off-peak")
	}
}

func TestPeakWindowWrapAndLabels(t *testing.T) {
	w := peakWindow{StartMin: 22*60 + 30, EndMin: 2 * 60}
	if !inWindow(w, 23*60) || !inWindow(w, 1*60) || inWindow(w, 3*60) {
		t.Fatalf("wrap-around window logic is wrong")
	}
	if got := clockLabel(7*60 + 30); got != "۰۷:۳۰" {
		t.Fatalf("clockLabel = %q", got)
	}
	if got := daysText([]int{1, 2, 3, 4, 5}); got != "دوشنبه تا جمعه" {
		t.Fatalf("daysText = %q", got)
	}
}

func TestPeakStoreDisable(t *testing.T) {
	p := newPeakStore(filepath.Join(t.TempDir(), "peakhours.json"))
	if err := p.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer p.close()
	p.disable("deepseek")
	mon0500 := time.Date(2026, time.September, 14, 5, 0, 0, 0, tehranLoc)
	if _, ok := p.peakEnd("deepseek", mon0500); ok {
		t.Fatalf("disabled provider must not report peak")
	}
}

func TestActiveProviderUsesSessionModel(t *testing.T) {
	b, _ := testBot(t, 1)
	// کانفیگ خالی تا fallback قابل‌پیش‌بینی باشد
	b.cfg.OCConfigHome = t.TempDir()
	b.cfg.OCDataHome = t.TempDir()
	oc := b.oc.(*fakeOC)
	oc.session = &occlient.Session{ID: "ses_1", ModelID: "deepseek/deepseek-flash"}

	b.stateFor(1, 10)
	b.setSession(1, "ses_1")
	if got := b.activeProvider(1); got != "deepseek" {
		t.Fatalf("activeProvider = %q; want deepseek", got)
	}

	// بدون نشست، به مدل کانفیگ (اینجا خالی) برمی‌گردد
	if got := b.activeProvider(2); got != "" {
		t.Fatalf("activeProvider(no session) = %q; want empty", got)
	}
}

func TestActiveProviderPrefersProviderID(t *testing.T) {
	b, _ := testBot(t, 1)
	oc := b.oc.(*fakeOC)
	// سرور فقط شناسهٔ مدل را می‌دهد (بدون اسلش) اما providerID درست است.
	s := &occlient.Session{ID: "ses_1", ModelID: "deepseek-flash"}
	s.Model.ID = "deepseek-flash"
	s.Model.ProviderID = "deepseek"
	oc.session = s

	b.stateFor(1, 10)
	b.setSession(1, "ses_1")
	if got := b.activeProvider(1); got != "deepseek" {
		t.Fatalf("activeProvider = %q; want deepseek (from providerID)", got)
	}
}

func TestStatusTextSilentWhenUnknown(t *testing.T) {
	p := newPeakStore(filepath.Join(t.TempDir(), "peakhours.json"))
	if err := p.load(); err != nil {
		t.Fatalf("load: %v", err)
	}
	defer p.close()
	if got := p.statusText("some-unknown-provider", time.Now()); got != "" {
		t.Fatalf("unknown provider should print nothing, got %q", got)
	}
	// پروایدر شناخته‌شده باید ثبت و نمایش داده شود.
	if got := p.statusText("deepseek", time.Now()); got == "" {
		t.Fatalf("deepseek status should not be empty")
	}
}

func TestProviderForModelFallsBackToCatalog(t *testing.T) {
	b, _ := testBot(t, 1)
	b.cat.parseOpencode([]byte(`{"all":[{"id":"deepseek","name":"DeepSeek","models":{"deepseek-flash":{"name":"DeepSeek Flash"}}}]}`))
	if got := b.providerFor("", "deepseek-flash"); got != "deepseek" {
		t.Fatalf("providerFor = %q; want deepseek (from catalog)", got)
	}
}
