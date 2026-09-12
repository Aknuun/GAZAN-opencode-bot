package main

import (
	"sync"
	"time"
)

// costInfo کش کوتاه‌مدت برچسب هزینه روی دکمهٔ «وضعیت» است.
type costInfo struct {
	when  time.Time
	label string
}

// peakPending پرامپتی است که به‌خاطر ساعات پیک هنوز اجرا نشده و منتظر تأیید
// کاربر است.
type peakPending struct {
	prompt string
	pid    string
}

// ssSel نشست‌های انتخاب‌شده برای یک عمل (حذف/ویرایش) را نگه می‌دارد.
type ssSel struct {
	action string
	sids   []string
}

// ssPick حالت انتخابگرِ دکمه‌ای نشست‌ها را نگه می‌دارد: عملِ انتخابی، صفحهٔ
// فعلی و شماره‌های تیک‌خورده.
type ssPick struct {
	action string
	page   int
	picked map[int]bool
}

// uiState داده‌های گذرای رابط کاربری (نه state ماندگار) هر چت را زیر قفل خودش
// نگه می‌دارد: صفحهٔ مدل‌های باز، جست‌وجو، صفحهٔ «نشست‌ها»، حالت گروهی و کش هزینه.
type uiState struct {
	mu   sync.Mutex
	mlc  map[int64]modelsCtx
	scx  map[int64]searchCtx
	ssp  map[int64]int
	ssl  map[int64][]string
	ssx  map[int64]ssSel
	spk  map[int64]ssPick
	gsm  map[int64]bool
	gsl  map[int64]map[string]bool
	cost map[int64]costInfo
	pk   map[int64]peakPending
}

func newUIState() *uiState {
	return &uiState{
		mlc:  map[int64]modelsCtx{},
		scx:  map[int64]searchCtx{},
		ssp:  map[int64]int{},
		ssl:  map[int64][]string{},
		ssx:  map[int64]ssSel{},
		spk:  map[int64]ssPick{},
		gsm:  map[int64]bool{},
		gsl:  map[int64]map[string]bool{},
		cost: map[int64]costInfo{},
		pk:   map[int64]peakPending{},
	}
}

// ---------- پرامپت معلق در ساعت پیک ----------

func (u *uiState) setPeakPending(chatID int64, p peakPending) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.pk[chatID] = p
}

func (u *uiState) takePeakPending(chatID int64) (peakPending, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	p, ok := u.pk[chatID]
	if ok {
		delete(u.pk, chatID)
	}
	return p, ok
}

// ---------- صفحهٔ مدل‌های باز و جست‌وجو ----------

func (u *uiState) setModelsCtx(chatID int64, c modelsCtx) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.mlc[chatID] = c
}

func (u *uiState) modelsCtx(chatID int64) (modelsCtx, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	c, ok := u.mlc[chatID]
	return c, ok
}

func (u *uiState) setSearchCtx(chatID int64, c searchCtx) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.scx[chatID] = c
}

func (u *uiState) searchCtx(chatID int64) (searchCtx, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	c, ok := u.scx[chatID]
	return c, ok
}

// ---------- صفحهٔ «🗂 نشست‌ها» ----------

func (u *uiState) setSSPage(chatID int64, page int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.ssp[chatID] = page
}

func (u *uiState) ssPage(chatID int64) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.ssp[chatID]
}

// setSSList ترتیبِ نمایش‌داده‌شدهٔ نشست‌ها را نگه می‌دارد تا شماره‌ای که کاربر
// بعداً می‌فرستد به همان نشست نگاشت شود.
func (u *uiState) setSSList(chatID int64, ids []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.ssl[chatID] = append([]string(nil), ids...)
}

func (u *uiState) ssList(chatID int64) []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	return append([]string(nil), u.ssl[chatID]...)
}

func (u *uiState) setSSSel(chatID int64, action string, sids []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.ssx[chatID] = ssSel{action: action, sids: append([]string(nil), sids...)}
}

func (u *uiState) ssSel(chatID int64) (string, []string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.ssx[chatID]
	return s.action, append([]string(nil), s.sids...)
}

func (u *uiState) clearSSSel(chatID int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.ssx, chatID)
}

// ---------- انتخابگر دکمه‌ای نشست‌ها ----------

func (u *uiState) startSSPick(chatID int64, action string, page int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.spk[chatID] = ssPick{action: action, page: page, picked: map[int]bool{}}
}

func (u *uiState) ssPick(chatID int64) (string, int, map[int]bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.spk[chatID]
	cp := make(map[int]bool, len(s.picked))
	for k, v := range s.picked {
		cp[k] = v
	}
	return s.action, s.page, cp
}

func (u *uiState) toggleSSPick(chatID int64, n int) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.spk[chatID]
	if s.picked == nil {
		s.picked = map[int]bool{}
	}
	if s.picked[n] {
		delete(s.picked, n)
	} else {
		s.picked[n] = true
	}
	u.spk[chatID] = s
}

func (u *uiState) setSSPickAll(chatID int64, ns []int, val bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	s := u.spk[chatID]
	if s.picked == nil {
		s.picked = map[int]bool{}
	}
	for _, n := range ns {
		if val {
			s.picked[n] = true
		} else {
			delete(s.picked, n)
		}
	}
	u.spk[chatID] = s
}

func (u *uiState) clearSSPick(chatID int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.spk, chatID)
}

// ---------- حالت انتخاب گروهی ----------

func (u *uiState) setGroupMode(chatID int64, on bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.gsm[chatID] = on
}

func (u *uiState) groupMode(chatID int64) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.gsm[chatID]
}

func (u *uiState) groupSel(chatID int64, sid string) bool {
	u.mu.Lock()
	defer u.mu.Unlock()
	return u.gsl[chatID][sid]
}

func (u *uiState) toggleGroupSel(chatID int64, sid string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.gsl[chatID] == nil {
		u.gsl[chatID] = map[string]bool{}
	}
	if u.gsl[chatID][sid] {
		delete(u.gsl[chatID], sid)
	} else {
		u.gsl[chatID][sid] = true
	}
}

func (u *uiState) clearGroupSel(chatID int64) {
	u.mu.Lock()
	defer u.mu.Unlock()
	delete(u.gsl, chatID)
}

func (u *uiState) groupSelCount(chatID int64) int {
	u.mu.Lock()
	defer u.mu.Unlock()
	return len(u.gsl[chatID])
}

func (u *uiState) groupSelList(chatID int64) []string {
	u.mu.Lock()
	defer u.mu.Unlock()
	var out []string
	for sid := range u.gsl[chatID] {
		out = append(out, sid)
	}
	return out
}

// ---------- کش هزینه (برچسب دکمهٔ وضعیت) ----------

func (u *uiState) costCached(userID int64) (string, bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	c, ok := u.cost[userID]
	if !ok || time.Since(c.when) >= 4*time.Second {
		return "", false
	}
	return c.label, true
}

func (u *uiState) storeCost(userID int64, label string) {
	u.mu.Lock()
	defer u.mu.Unlock()
	u.cost[userID] = costInfo{when: time.Now(), label: label}
}
