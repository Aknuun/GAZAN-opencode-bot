package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// ---------- نرخ ارز (دلار → تومان) ----------

// نرخ یک بار در روز گرفته می‌شود؛ کافی است و شبکه را بیهوده شلوغ نمی‌کند.
const (
	fxTTL          = 24 * time.Hour
	fxFetchTimeout = 25 * time.Second
	fxReqTimeout   = 10 * time.Second
)

// fxStore نرخ دلار به تومان را کش می‌کند و هر fxTTL یک بار تازه‌سازی می‌کند.
// اگر تازه‌سازی ناموفق باشد نرخ قبلی/صفر نگه داشته می‌شود و تا پایان TTL دوباره
// تلاش خودکار نمی‌شود (تا هر پیام درخواست شبکه نزند).
type fxStore struct {
	mu sync.Mutex
	// فیلدهای قابل تزریق برای تست
	ttl   time.Duration
	now   func() time.Time
	fetch func(context.Context) (float64, error)

	inflight atomic.Bool
	rate     float64 // تومان به ازای هر ۱ دلار
	at       time.Time
}

func newFXStore() *fxStore {
	return &fxStore{ttl: fxTTL, now: time.Now, fetch: fetchTomanPerUSD}
}

// refresh نرخ را همین حالا می‌گیرد و کش را به‌روز می‌کند. زمان تلاش در هر دو
// حالت موفق/ناموفق ثبت می‌شود تا تلاش مجدد پشت‌سرهم رخ ندهد.
func (f *fxStore) refresh(ctx context.Context) error {
	r, err := f.fetch(ctx)
	f.mu.Lock()
	f.at = f.now()
	if err == nil && r > 0 {
		f.rate = r
	}
	f.mu.Unlock()
	return err
}

// rate نرخ کش‌شده را برمی‌گرداند. اگر کش کهنه بود یک تازه‌سازی در پس‌زمینه
// شروع می‌کند ولی پاسخ را مسدود نمی‌کند (نرخ قبلی/صفر برمی‌گردد).
func (f *fxStore) currentRate() float64 {
	f.mu.Lock()
	rate := f.rate
	at := f.at
	fresh := !at.IsZero() && f.now().Sub(at) < f.ttl
	f.mu.Unlock()
	if fresh {
		return rate
	}
	if !f.inflight.CompareAndSwap(false, true) {
		return rate
	}
	go func() {
		defer f.inflight.Store(false)
		ctx, cancel := context.WithTimeout(context.Background(), fxFetchTimeout)
		defer cancel()
		if err := f.refresh(ctx); err != nil {
			slog.Warn("دریافت نرخ دلار ناموفق بود (نمایش با دلار ادامه می‌یابد)", "error", err)
		}
	}()
	return rate
}

// tomanFor تبدیل مبلغ دلاری به تومان (با نرخ کش‌شده)؛ بدون نرخ صفر برمی‌گرداند.
func (f *fxStore) tomanFor(usd float64) int64 {
	if usd <= 0 {
		return 0
	}
	r := f.currentRate()
	if r <= 0 {
		return 0
	}
	return int64(math.Round(usd * r))
}

// keepWarm تازه‌سازی را بلافاصله در شروع اجرا و بعد هر fxTTL انجام می‌دهد.
func (f *fxStore) keepWarm() {
	ctx, cancel := context.WithTimeout(context.Background(), fxFetchTimeout)
	if err := f.refresh(ctx); err != nil {
		slog.Warn("دریافت نرخ دلار در شروع ناموفق بود", "error", err)
	}
	cancel()
	for {
		time.Sleep(f.ttl)
		ctx, cancel := context.WithTimeout(context.Background(), fxFetchTimeout)
		err := f.refresh(ctx)
		cancel()
		if err != nil {
			slog.Warn("به‌روزرسانی روزانهٔ نرخ دلار ناموفق بود", "error", err)
		}
	}
}

// fetchTomanPerUSD نرخ بازارِ تومان را می‌گیرد؛ اول والکس (قیمت تتر-تومان)، بعد
// نوبیتکس (ریال/۱۰) و در آخر نرخ رسمی ارز (IRR). خروجی: تومان به ازای هر ۱ دلار.
func fetchTomanPerUSD(ctx context.Context) (float64, error) {
	if v, err := fetchWallex(ctx); err == nil && v > 0 {
		return v, nil
	}
	if v, err := fetchNobitex(ctx); err == nil && v > 0 {
		return v, nil
	}
	return fetchIRR(ctx)
}

// fetchWallex قیمت تتر-تومان (USDTTMN) را از والکس می‌گیرد. قیمت بازار است و
// مستقیم «تومان به ازای هر دلار» برمی‌گرداند.
func fetchWallex(ctx context.Context) (float64, error) {
	nctx, cancel := context.WithTimeout(ctx, fxReqTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(nctx, http.MethodGet, "https://api.wallex.ir/v1/markets", nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("wallex: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		return 0, err
	}
	return wallexToman(body)
}

func wallexToman(body []byte) (float64, error) {
	var parsed struct {
		Result struct {
			Symbols map[string]struct {
				Stats struct {
					LastPrice json.RawMessage `json:"lastPrice"`
				} `json:"stats"`
			} `json:"symbols"`
		} `json:"result"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, err
	}
	s, ok := parsed.Result.Symbols["USDTTMN"]
	if !ok {
		return 0, errors.New("wallex: USDTTMN یافت نشد")
	}
	last, err := parseJSONNum(s.Stats.LastPrice)
	if err != nil || last <= 0 {
		return 0, errors.New("wallex: قیمت نامعتبر")
	}
	return last, nil
}

// fetchNobitex قیمت USDT (≈دلار) را از نوبیتکس به ریال می‌گیرد و به تومان برمی‌گرداند.
func fetchNobitex(ctx context.Context) (float64, error) {
	nctx, cancel := context.WithTimeout(ctx, fxReqTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(nctx, http.MethodGet, "https://api.nobitex.ir/market/stats?srcCurrency=usdt&dstCurrency=rls", nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("nobitex: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	return nobitexToman(body)
}

func nobitexToman(body []byte) (float64, error) {
	var parsed struct {
		Stats map[string]struct {
			Latest json.RawMessage `json:"latest"`
		} `json:"stats"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, err
	}
	s, ok := parsed.Stats["usdt-rls"]
	if !ok {
		return 0, errors.New("nobitex: usdt-rls یافت نشد")
	}
	rls, err := parseJSONNum(s.Latest)
	if err != nil || rls <= 0 {
		return 0, errors.New("nobitex: قیمت نامعتبر")
	}
	return rls / 10, nil // ریال → تومان
}

// fetchIRR نرخ رسمی IRR را از open.er-api می‌گیرد و به تومان برمی‌گرداند.
func fetchIRR(ctx context.Context) (float64, error) {
	nctx, cancel := context.WithTimeout(ctx, fxReqTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(nctx, http.MethodGet, "https://open.er-api.com/v6/latest/USD", nil)
	if err != nil {
		return 0, err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return 0, fmt.Errorf("er-api: %s", resp.Status)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, err
	}
	return erapiToman(body)
}

func erapiToman(body []byte) (float64, error) {
	var parsed struct {
		Rates map[string]float64 `json:"rates"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return 0, err
	}
	irr, ok := parsed.Rates["IRR"]
	if !ok || irr <= 0 {
		return 0, errors.New("er-api: نرخ IRR یافت نشد")
	}
	return irr / 10, nil // ریال → تومان
}

// parseJSONNum عدد JSON را چه عددی باشد چه رشته‌ای عددی، پارس می‌کند.
func parseJSONNum(raw json.RawMessage) (float64, error) {
	var f float64
	if err := json.Unmarshal(raw, &f); err == nil {
		return f, nil
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		if v, err := strconv.ParseFloat(strings.TrimSpace(s), 64); err == nil {
			return v, nil
		}
	}
	return 0, errors.New("عدد نامعتبر")
}

// faSep عدد را با جداکنندهٔ هزارگان و رقم‌های فارسی می‌نویسد (مثل ۱۲٬۳۴۵).
func faSep(v int64) string {
	s := strconv.FormatInt(v, 10)
	var groups []string
	for len(s) > 3 {
		groups = append([]string{s[len(s)-3:]}, groups...)
		s = s[:len(s)-3]
	}
	if len(s) > 0 {
		groups = append([]string{s}, groups...)
	}
	var sb strings.Builder
	for i, g := range groups {
		if i > 0 {
			sb.WriteRune('٬')
		}
		for _, r := range g {
			sb.WriteRune(rune('۰' + (r - '0')))
		}
	}
	return sb.String()
}

// ---------- قالب‌بندی هزینه روی Bot ----------

// tomanText «۱۲۳ تومان» را برمی‌گرداند؛ اگر نرخ در دسترس نبود رشتهٔ خالی.
func (b *Bot) tomanText(usd float64) string {
	t := b.fx.tomanFor(usd)
	if t <= 0 {
		return ""
	}
	return faSep(t) + " تومان"
}

// costText «$0.0123 (≈ ۸۶۴ تومان)»؛ بدون نرخ فقط دلار را نشان می‌دهد.
func (b *Bot) costText(usd float64) string {
	base := fmt.Sprintf("$%.4f", usd)
	if t := b.tomanText(usd); t != "" {
		return base + " (≈ " + t + ")"
	}
	return base
}

// costButton متن دکمهٔ «وضعیت و هزینه»: بدون توکن، با دلار و تومان.
func (b *Bot) costButton(usd float64) string {
	base := fmt.Sprintf("$%.4f", usd)
	if t := b.tomanText(usd); t != "" {
		return base + " · " + t
	}
	return base
}
