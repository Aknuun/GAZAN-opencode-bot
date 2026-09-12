package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"opencode-tg-bot/internal/statefile"
)

// ---------- ساعت پیک مصرف پروایدرها ----------
//
// بعضی پروایدرها در ساعات خاصی گران‌تر (پیک) هستند و در بقیهٔ ساعات تخفیف
// می‌دهند. این فایل بازه‌های پیک را به وقت ایران نگه می‌دارد تا ربات قبل از
// اجرا هشدار بدهد و کاربر تصمیم بگیرد.

// peakWindow یک بازهٔ پیک به وقت ایران است.
// Days روزهای هفته بر اساس time.Weekday (یکشنبه=0 … شنبه=6)؛ خالی یعنی هر روز.
type peakWindow struct {
	StartMin int   `json:"start_min"` // دقیقه از نیمه‌شب
	EndMin   int   `json:"end_min"`
	Days     []int `json:"days,omitempty"`
}

// peakProvider تنظیمات پیک یک پروایدر
type peakProvider struct {
	Windows []peakWindow `json:"windows"`
	Enabled bool         `json:"enabled"`
	Source  string       `json:"source,omitempty"` // builtin | user
}

// builtinPeaks اطلاعات ساعت پیک پروایدرهایی است که از منبع رسمی‌شان خوانده
// شده. DeepSeek: پیک ۰۱:۰۰–۰۴:۰۰ و ۰۶:۰۰–۱۰:۰۰ UTC، دوشنبه تا جمعه؛ معادل
// ۰۴:۳۰–۰۷:۳۰ و ۰۹:۳۰–۱۳:۳۰ به وقت ایران.
var builtinPeaks = map[string][]peakWindow{
	"deepseek": {
		{StartMin: 4*60 + 30, EndMin: 7*60 + 30, Days: []int{1, 2, 3, 4, 5}},
		{StartMin: 9*60 + 30, EndMin: 13*60 + 30, Days: []int{1, 2, 3, 4, 5}},
	},
}

// peakStore تنظیمات پیک را روی دیسک نگه می‌دارد و دسترسی هم‌زمان را قفل می‌کند.
type peakStore struct {
	mu    sync.Mutex
	path  string
	cfg   map[string]*peakProvider
	saver *statefile.Saver
}

func newPeakStore(path string) *peakStore {
	p := &peakStore{path: path, cfg: map[string]*peakProvider{}}
	p.saver = statefile.NewSaver(path, func() ([]byte, error) {
		p.mu.Lock()
		defer p.mu.Unlock()
		return json.MarshalIndent(p.cfg, "", "  ")
	})
	p.saver.SetErrorHandler(func(err error) {
		slog.Error("ذخیرهٔ ساعت پیک ناموفق بود", "path", path, "error", err)
	})
	return p
}

// load تنظیمات ذخیره‌شده را می‌خواند و پروایدرهای داخلی را در صورت نبود می‌افزاید.
func (p *peakStore) load() error {
	data, err := os.ReadFile(p.path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	} else {
		var cfg map[string]*peakProvider
		if err := json.Unmarshal(data, &cfg); err != nil {
			return err
		}
		p.mu.Lock()
		p.cfg = cfg
		p.mu.Unlock()
	}

	p.mu.Lock()
	if p.cfg == nil {
		p.cfg = map[string]*peakProvider{}
	}
	changed := false
	for pid, def := range builtinPeaks {
		cur, ok := p.cfg[pid]
		if !ok {
			p.cfg[pid] = &peakProvider{Windows: def, Enabled: true, Source: "builtin"}
			changed = true
			continue
		}
		if len(cur.Windows) == 0 {
			cur.Windows = def
			changed = true
		}
	}
	p.mu.Unlock()
	if changed {
		p.saver.MarkDirty()
	}
	return nil
}

func (p *peakStore) close() error { return p.saver.Close() }

// windowsFor بازه‌های پیک یک پروایدر را برمی‌گرداند (تنظیم کاربر مقدم است).
func (p *peakStore) windowsFor(pid string) []peakWindow {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cur, ok := p.cfg[pid]; ok && len(cur.Windows) > 0 {
		return append([]peakWindow(nil), cur.Windows...)
	}
	if def, ok := builtinPeaks[pid]; ok {
		return append([]peakWindow(nil), def...)
	}
	return nil
}

// enabledFor می‌گوید یادآوری پیک این پروایدر روشن است یا نه؛ پیش‌فرض پروایدرهای
// داخلی روشن است.
func (p *peakStore) enabledFor(pid string) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if cur, ok := p.cfg[pid]; ok {
		return cur.Enabled
	}
	_, ok := builtinPeaks[pid]
	return ok
}

// ensure بازه‌های پیک داخلیِ یک پروایدر را (اگر ثبت نشده باشد) ثبت و فعال
// می‌کند تا در پایان هر چت، ساعت پیک شناخته‌شده‌اش «ثبت» شود.
func (p *peakStore) ensure(pid string) {
	def, ok := builtinPeaks[pid]
	if !ok {
		return
	}
	p.mu.Lock()
	if p.cfg == nil {
		p.cfg = map[string]*peakProvider{}
	}
	changed := false
	if cur, ok := p.cfg[pid]; !ok {
		p.cfg[pid] = &peakProvider{Windows: append([]peakWindow(nil), def...), Enabled: true, Source: "builtin"}
		changed = true
	} else if len(cur.Windows) == 0 {
		cur.Windows = append([]peakWindow(nil), def...)
		cur.Source = "builtin"
		changed = true
	}
	p.mu.Unlock()
	if changed {
		p.saver.MarkDirty()
	}
}

// enable فعال‌کردن یادآوری با بازه‌های مشخص
func (p *peakStore) enable(pid string, ws []peakWindow) {
	p.mu.Lock()
	if p.cfg == nil {
		p.cfg = map[string]*peakProvider{}
	}
	cur := p.cfg[pid]
	if cur == nil {
		cur = &peakProvider{}
		p.cfg[pid] = cur
	}
	cur.Windows = ws
	cur.Enabled = true
	cur.Source = "user"
	p.mu.Unlock()
	p.saver.MarkDirty()
}

// disable خاموش‌کردن یادآوری پیک یک پروایدر
func (p *peakStore) disable(pid string) {
	p.mu.Lock()
	if p.cfg == nil {
		p.cfg = map[string]*peakProvider{}
	}
	cur := p.cfg[pid]
	if cur == nil {
		cur = &peakProvider{}
		p.cfg[pid] = cur
	}
	cur.Enabled = false
	p.mu.Unlock()
	p.saver.MarkDirty()
}

// peakEnd اگر همین حالا در بازهٔ پیک این پروایدر باشیم، دقیقهٔ پایان پیک را
// برمی‌گرداند.
func (p *peakStore) peakEnd(pid string, now time.Time) (int, bool) {
	if !p.enabledFor(pid) {
		return 0, false
	}
	ws := p.windowsFor(pid)
	if len(ws) == 0 {
		return 0, false
	}
	t := now.In(tehranLoc)
	wd := int(t.Weekday())
	cur := t.Hour()*60 + t.Minute()
	for _, w := range ws {
		if !dayMatch(w.Days, wd) {
			continue
		}
		if inWindow(w, cur) {
			return w.EndMin, true
		}
	}
	return 0, false
}

func inWindow(w peakWindow, cur int) bool {
	s, e := normMin(w.StartMin), normMin(w.EndMin)
	if s == e {
		return false
	}
	if s < e {
		return cur >= s && cur < e
	}
	return cur >= s || cur < e // عبور از نیمه‌شب
}

func dayMatch(days []int, wd int) bool {
	if len(days) == 0 {
		return true
	}
	for _, d := range days {
		if d == wd {
			return true
		}
	}
	return false
}

func normMin(m int) int {
	return ((m % 1440) + 1440) % 1440
}

// ---------- متن‌های نمایشی ----------

var faWeekdays = [...]string{"یکشنبه", "دوشنبه", "سه‌شنبه", "چهارشنبه", "پنجشنبه", "جمعه", "شنبه"}

// clockLabel دقیقه از نیمه‌شب را به «ساعت:دقیقه» فارسی تبدیل می‌کند.
func clockLabel(m int) string {
	m = normMin(m)
	return faClock(m/60) + ":" + faClock(m%60)
}

func daysText(days []int) string {
	if len(days) == 0 {
		return "هر روز"
	}
	sorted := append([]int(nil), days...)
	sort.Ints(sorted)
	contig := true
	for i := 1; i < len(sorted); i++ {
		if sorted[i] != sorted[i-1]+1 {
			contig = false
			break
		}
	}
	if contig && len(sorted) > 1 {
		return faWeekdays[sorted[0]] + " تا " + faWeekdays[sorted[len(sorted)-1]]
	}
	var names []string
	for _, d := range sorted {
		if d >= 0 && d < len(faWeekdays) {
			names = append(names, faWeekdays[d])
		}
	}
	return strings.Join(names, "، ")
}

// formatWindows بازه‌های پیک را به متن خوانا تبدیل می‌کند.
func formatWindows(ws []peakWindow) string {
	var parts []string
	for _, w := range ws {
		parts = append(parts, fmt.Sprintf("%s، از %s تا %s (به وقت ایران)", daysText(w.Days), clockLabel(w.StartMin), clockLabel(w.EndMin)))
	}
	return strings.Join(parts, "\n")
}

// peakProviderName نام کوتاه و خوانای پروایدر
func peakProviderName(pid string) string {
	switch pid {
	case "deepseek":
		return "دیپ‌سیک (DeepSeek)"
	case "openai":
		return "ChatGPT (OpenAI)"
	case "anthropic":
		return "Claude (Anthropic)"
	case "google":
		return "Gemini (Google)"
	case "xai":
		return "Grok (xAI)"
	case "openrouter":
		return "OpenRouter"
	}
	if p, ok := presetByID(pid); ok {
		return p.label
	}
	return pid
}

// peakPromptText همان پیامی است که قبل از اجرا در ساعات پیک نمایش داده می‌شود.
func peakPromptText(pid string, endMin int) string {
	return fmt.Sprintf("⏰ الان تا ساعت %s به وقت ایران در ساعات پیک مصرف و گرونِ %s قرار داریم.\nمی‌خوای ادامه بدیم؟", clockLabel(endMin), peakProviderName(pid))
}

// statusText خلاصهٔ وضعیت پیکِ پروایدر را برای پایان هر چت می‌سازد.
func (p *peakStore) statusText(pid string, now time.Time) string {
	if pid == "" {
		return ""
	}
	// اگر پروایدرِ شناخته‌شده‌ای باشد ولی هنوز ثبت نشده، همین‌جا ثبتش کن.
	p.ensure(pid)
	name := peakProviderName(pid)
	ws := p.windowsFor(pid)
	if len(ws) == 0 {
		// ناشناس: هیچ ادعایی نکن و چیزی نشان نده.
		return ""
	}
	if !p.enabledFor(pid) {
		return "⏰ یادآوری ساعت پیک «" + name + "» خاموش است."
	}
	base := "⏰ پیک " + name + ": " + strings.ReplaceAll(formatWindows(ws), "\n", "، ")
	if end, ok := p.peakEnd(pid, now); ok {
		return base + fmt.Sprintf("\n🔴 همین حالا در ساعات پیک هستیم (تا %s به وقت ایران).", clockLabel(end))
	}
	return base + "\n🟢 الان خارج از ساعات پیک هستیم."
}

// normalizeProvider شناسهٔ پروایدر را یکسان‌سازی می‌کند تا نام‌های معادل
// (مثل google-vertex یا anthropic) به کلید یکسان برسند.
func normalizeProvider(pid string) string {
	switch strings.ToLower(strings.TrimSpace(pid)) {
	case "google-vertex", "vertex", "google-ai", "googleapis":
		return "google"
	case "openai-compatible", "chatgpt":
		return "openai"
	case "claude":
		return "anthropic"
	case "grok":
		return "xai"
	default:
		return strings.ToLower(strings.TrimSpace(pid))
	}
}
