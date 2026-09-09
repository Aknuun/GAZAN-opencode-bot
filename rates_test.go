package main

import (
	"context"
	"testing"
	"time"
)

func TestWallexToman(t *testing.T) {
	body := []byte(`{"result":{"symbols":{"USDTTMN":{"symbol":"USDTTMN","stats":{"lastPrice":"232002.0000000000000000"}}}}}`)
	got, err := wallexToman(body)
	if err != nil {
		t.Fatal(err)
	}
	if got != 232002 {
		t.Fatalf("got %v want 232002", got)
	}
}

func TestWallexMissingSymbol(t *testing.T) {
	if _, err := wallexToman([]byte(`{"result":{"symbols":{}}}`)); err == nil {
		t.Fatal("expected error when USDTTMN missing")
	}
}

func TestNobitexToman(t *testing.T) {
	// قیمت USDT به ریال مثلاً ۷۰۰٬۰۰۰ ریال → ۷۰٬۰۰۰ تومان
	body := []byte(`{"status":"ok","stats":{"usdt-rls":{"latest":700000}}}`)
	got, err := nobitexToman(body)
	if err != nil {
		t.Fatal(err)
	}
	if got != 70000 {
		t.Fatalf("got %v want 70000", got)
	}
}

func TestNobitexTomanStringNumber(t *testing.T) {
	// بعضی پاسخ‌ها عدد را به‌صورت رشته می‌دهند
	body := []byte(`{"stats":{"usdt-rls":{"latest":"700000"}}}`)
	got, err := nobitexToman(body)
	if err != nil {
		t.Fatal(err)
	}
	if got != 70000 {
		t.Fatalf("got %v want 70000", got)
	}
}

func TestERAPIToman(t *testing.T) {
	body := []byte(`{"result":"success","rates":{"USD":1,"IRR":420000}}`)
	got, err := erapiToman(body)
	if err != nil {
		t.Fatal(err)
	}
	if got != 42000 {
		t.Fatalf("got %v want 42000", got)
	}
}

func TestERAPIMissingIRR(t *testing.T) {
	if _, err := erapiToman([]byte(`{"rates":{"USD":1}}`)); err == nil {
		t.Fatal("expected error when IRR missing")
	}
}

func TestFXStoreCachesUntilTTL(t *testing.T) {
	calls := 0
	f := &fxStore{
		ttl: 24 * time.Hour,
		now: time.Now,
		fetch: func(context.Context) (float64, error) {
			calls++
			return 60000, nil
		},
	}
	if err := f.refresh(context.Background()); err != nil {
		t.Fatal(err)
	}
	// نرخ تازه است؛ نباید درخواست شبکهٔ دیگری بزند
	for i := 0; i < 5; i++ {
		if r := f.currentRate(); r != 60000 {
			t.Fatalf("rate=%v", r)
		}
	}
	if calls != 1 {
		t.Fatalf("expected 1 fetch, got %d", calls)
	}
	// نرخ تازه هنوز باید در دسترس باشد
	if got := f.tomanFor(0.0042); got != 252 {
		t.Fatalf("tomanFor(0.0042)=%d want 252", got)
	}
}

func TestFXStoreFailureKeepsOldAndCooldown(t *testing.T) {
	f := &fxStore{
		ttl: time.Hour,
		now: time.Now,
		fetch: func(context.Context) (float64, error) {
			return 0, errFake
		},
	}
	if err := f.refresh(context.Background()); err == nil {
		t.Fatal("expected refresh error")
	}
	if r := f.currentRate(); r != 0 {
		t.Fatalf("rate should stay 0 after failure, got %v", r)
	}
}

func TestFaSep(t *testing.T) {
	cases := map[int64]string{
		0:          "۰",
		5:          "۵",
		123:        "۱۲۳",
		1234:       "۱٬۲۳۴",
		1234567:    "۱٬۲۳۴٬۵۶۷",
		1000000000: "۱٬۰۰۰٬۰۰۰٬۰۰۰",
	}
	for in, want := range cases {
		if got := faSep(in); got != want {
			t.Fatalf("faSep(%d)=%q want %q", in, got, want)
		}
	}
}
