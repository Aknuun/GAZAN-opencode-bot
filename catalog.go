package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// ---------- کاتالوگ مدل‌ها (همان models.dev که خود opencode استفاده می‌کند) ----------

const catalogURL = "https://models.dev/api.json"
const catalogTTL = 24 * time.Hour

// هر چند وقت یک‌بار مدل‌ها را از خود سرور opencode تازه کنیم تا همیشه
// با مدل‌های واقعیِ opencode (شامل پروایدرهای سفارشی) سینک باشد.
const opencodeSyncTTL = 2 * time.Minute

// catModel یک مدل شناخته‌شده در کاتالوگ
type catModel struct {
	ID   string
	Name string
}

// catProv یک پروایدر کاتالوگ به‌همراه مدل‌هایش
type catProv struct {
	ID     string
	Name   string
	Models []catModel
}

type modelCatalog struct {
	mu        sync.Mutex
	load      sync.Mutex // بارگذاری تکی (singleflight ساده)
	cachePath string
	baseURL   string    // آدرس سرور opencode برای سینک زندهٔ مدل‌ها
	provs     []catProv // مرتب‌شده بر اساس نام
	byID      map[string]*catProv
	loadedAt  time.Time
	syncedAt  time.Time // آخرین تلاش برای سینک با opencode
	source    string    // "opencode" یا "models.dev"
	failed    error
}

func newModelCatalog(cachePath string, baseURL ...string) *modelCatalog {
	c := &modelCatalog{cachePath: cachePath, byID: map[string]*catProv{}}
	if len(baseURL) > 0 {
		c.baseURL = strings.TrimRight(baseURL[0], "/")
	}
	return c
}

// ensure کاتالوگ را بارگذاری می‌کند: اول سینک زنده با خود opencode،
// بعد کش/دانلود models.dev؛ اگر دانلود خطا داد کش قدیمی را هم قبول می‌کند.
func (c *modelCatalog) ensure() error {
	c.load.Lock()
	defer c.load.Unlock()

	c.mu.Lock()
	hasData := len(c.byID) > 0
	fresh := hasData && time.Since(c.loadedAt) < catalogTTL
	trySync := c.baseURL != "" && time.Since(c.syncedAt) >= opencodeSyncTTL
	c.mu.Unlock()

	// ۱) منبع معتبر: خود سرور opencode (شامل پروایدرهای سفارشی کانفیگ)
	if trySync {
		c.mu.Lock()
		c.syncedAt = time.Now()
		c.mu.Unlock()
		if raw, err := fetchOpencodeProviders(c.baseURL); err == nil {
			if err := c.parseOpencode(raw); err == nil {
				return nil
			}
		}
	}

	if fresh {
		return nil
	}

	// ۲) کش تازهٔ models.dev
	if raw, ok := c.readRaw(); ok && time.Since(c.fileModTime()) < catalogTTL {
		if err := c.parse(raw); err == nil {
			return nil
		}
	}
	// ۳) دانلود models.dev
	raw, err := fetchCatalog()
	if err != nil {
		// کش قدیمی بهتر از هیچ است
		if raw2, ok := c.readRaw(); ok {
			if perr := c.parse(raw2); perr == nil {
				return nil
			}
		}
		c.mu.Lock()
		c.failed = err
		c.mu.Unlock()
		return fmt.Errorf("کاتالوگ مدل‌ها در دسترس نیست: %v", err)
	}
	c.saveRaw(raw)
	return c.parse(raw)
}

func (c *modelCatalog) fileModTime() time.Time {
	st, err := os.Stat(c.cachePath)
	if err != nil {
		return time.Time{}
	}
	return st.ModTime()
}

func (c *modelCatalog) readRaw() ([]byte, bool) {
	b, err := os.ReadFile(c.cachePath)
	if err != nil {
		return nil, false
	}
	if len(b) == 0 {
		return nil, false
	}
	return b, true
}

func (c *modelCatalog) saveRaw(raw []byte) {
	if c.cachePath == "" {
		return
	}
	if err := os.MkdirAll(filepath.Dir(c.cachePath), 0o755); err != nil {
		return
	}
	os.WriteFile(c.cachePath, raw, 0o644)
}

func fetchCatalog() ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(catalogURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("models.dev: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 30<<20))
}

// fetchOpencodeProviders فهرست پروایدر/مدل‌های خود سرور opencode را از
// endpoint استاندارد /provider می‌گیرد؛ این دقیقاً همان چیزی است که opencode
// می‌شناسد (مدل‌های models.dev + پروایدرهای سفارشی کانفیگ).
func fetchOpencodeProviders(baseURL string) ([]byte, error) {
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Get(strings.TrimRight(baseURL, "/") + "/provider")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("opencode /provider: %s", resp.Status)
	}
	return io.ReadAll(io.LimitReader(resp.Body, 60<<20))
}

// parse خروجی api.json را به ساختار داخلی تبدیل می‌کند
func (c *modelCatalog) parse(raw []byte) error {
	var m map[string]struct {
		Name   string                     `json:"name"`
		Models map[string]json.RawMessage `json:"models"`
	}
	if err := json.Unmarshal(raw, &m); err != nil {
		return fmt.Errorf("پارس کاتالوگ: %v", err)
	}
	provs := make([]catProv, 0, len(m))
	byID := make(map[string]*catProv, len(m))
	for pid, pr := range m {
		cp := &catProv{ID: pid, Name: pr.Name}
		if cp.Name == "" {
			cp.Name = pid
		}
		for mid := range pr.Models {
			name := mid
			var meta struct {
				Name string `json:"name"`
			}
			if raw2, ok := pr.Models[mid]; ok {
				if err := json.Unmarshal(raw2, &meta); err == nil && meta.Name != "" {
					name = meta.Name
				}
			}
			cp.Models = append(cp.Models, catModel{ID: mid, Name: name})
		}
		sort.Slice(cp.Models, func(i, j int) bool {
			return strings.ToLower(cp.Models[i].ID) < strings.ToLower(cp.Models[j].ID)
		})
		if len(cp.Models) > 0 {
			provs = append(provs, *cp)
		}
	}
	sort.Slice(provs, func(i, j int) bool {
		return strings.ToLower(provs[i].Name) < strings.ToLower(provs[j].Name)
	})
	for i := range provs {
		byID[provs[i].ID] = &provs[i]
	}
	c.mu.Lock()
	c.provs = provs
	c.byID = byID
	c.loadedAt = time.Now()
	c.source = "models.dev"
	c.failed = nil
	c.mu.Unlock()
	return nil
}

// parseOpencode خروجی /provider سرور opencode را به ساختار داخلی تبدیل می‌کند.
func (c *modelCatalog) parseOpencode(raw []byte) error {
	var payload struct {
		All []struct {
			ID     string `json:"id"`
			Name   string `json:"name"`
			Models map[string]struct {
				Name string `json:"name"`
			} `json:"models"`
		} `json:"all"`
	}
	if err := json.Unmarshal(raw, &payload); err != nil {
		return fmt.Errorf("پارس کاتالوگ opencode: %v", err)
	}
	if len(payload.All) == 0 {
		return fmt.Errorf("کاتالوگ opencode خالی است")
	}
	provs := make([]catProv, 0, len(payload.All))
	for _, p := range payload.All {
		cp := catProv{ID: p.ID, Name: p.Name}
		if cp.Name == "" {
			cp.Name = p.ID
		}
		for mid, m := range p.Models {
			name := m.Name
			if name == "" {
				name = mid
			}
			cp.Models = append(cp.Models, catModel{ID: mid, Name: name})
		}
		sort.Slice(cp.Models, func(i, j int) bool {
			return strings.ToLower(cp.Models[i].ID) < strings.ToLower(cp.Models[j].ID)
		})
		if len(cp.Models) > 0 {
			provs = append(provs, cp)
		}
	}
	sort.Slice(provs, func(i, j int) bool {
		return strings.ToLower(provs[i].Name) < strings.ToLower(provs[j].Name)
	})
	byID := make(map[string]*catProv, len(provs))
	for i := range provs {
		byID[provs[i].ID] = &provs[i]
	}
	c.mu.Lock()
	c.provs = provs
	c.byID = byID
	c.loadedAt = time.Now()
	c.source = "opencode"
	c.failed = nil
	c.mu.Unlock()
	return nil
}

// providers فهرست همهٔ پروایدرهای کاتالوگ
func (c *modelCatalog) providers() ([]catProv, error) {
	if err := c.ensure(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]catProv(nil), c.provs...), nil
}

func (c *modelCatalog) provider(id string) (*catProv, bool) {
	if err := c.ensure(); err != nil {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.byID[id]
	if !ok {
		return nil, false
	}
	out := *p
	return &out, true
}

// providerOfModel پروایدری را پیدا می‌کند که مدل با شناسهٔ model را دارد. برای
// وقتی که سرور فقط شناسهٔ مدل را بدهد و پروایدرش را نداشته باشیم.
func (c *modelCatalog) providerOfModel(model string) (string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", false
	}
	if i := strings.IndexByte(model, '/'); i > 0 {
		model = model[i+1:]
	}
	if err := c.ensure(); err != nil {
		return "", false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, p := range c.provs {
		for _, md := range p.Models {
			id := md.ID
			if i := strings.IndexByte(id, '/'); i > 0 {
				id = id[i+1:]
			}
			if strings.EqualFold(id, model) {
				return p.ID, true
			}
		}
	}
	return "", false
}

// searchProvs پروایدرهایی را برمی‌گرداند که نام/شناسه‌شان یا یکی از مدل‌هاشان
// شامل query باشد.
func (c *modelCatalog) searchProvs(q string) ([]catProv, error) {
	if err := c.ensure(); err != nil {
		return nil, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	q = strings.ToLower(strings.TrimSpace(q))
	var out []catProv
	for _, p := range c.provs {
		if q == "" {
			out = append(out, p)
			continue
		}
		matched := strings.Contains(strings.ToLower(p.ID), q) || strings.Contains(strings.ToLower(p.Name), q)
		if !matched {
			for _, md := range p.Models {
				if strings.Contains(strings.ToLower(md.ID), q) || strings.Contains(strings.ToLower(md.Name), q) {
					matched = true
					break
				}
			}
		}
		if matched {
			out = append(out, p)
		}
	}
	return out, nil
}

// modelsOf مدل‌های یک پروایدر را با فیلتر اختیاری برمی‌گرداند
func (c *modelCatalog) modelsOf(pid, filter string) ([]catModel, bool, error) {
	if err := c.ensure(); err != nil {
		return nil, false, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	p, ok := c.byID[pid]
	if !ok {
		return nil, false, nil
	}
	f := strings.ToLower(strings.TrimSpace(filter))
	if f == "" {
		return append([]catModel(nil), p.Models...), true, nil
	}
	var out []catModel
	for _, md := range p.Models {
		if strings.Contains(strings.ToLower(md.ID), f) || strings.Contains(strings.ToLower(md.Name), f) {
			out = append(out, md)
		}
	}
	return out, true, nil
}
