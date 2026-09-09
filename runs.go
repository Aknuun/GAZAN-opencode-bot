package main

import "sync"

// runManager مالک اجراهای نشست‌هاست و آن‌ها را زیر قفل خودش نگه می‌دارد.
// فقط یک اجرا در یک زمان مجاز است (تک‌کاره).
type runManager struct {
	mu        sync.Mutex
	runs      map[string]*runCtl   // کلید = نشست opencode
	approvals map[string]chan bool // sid → کانال تأییدِ طرح (هنگام انتظار «شروع کن؟»)
}

func newRunManager() *runManager {
	return &runManager{runs: map[string]*runCtl{}, approvals: map[string]chan bool{}}
}

func (m *runManager) get(sid string) (*runCtl, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[sid]
	return r, ok
}

// total تعداد اجراهای فعال (در حال انجام یا در انتظار تأیید) را می‌دهد.
func (m *runManager) total() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.runs)
}

// activeSID نشستِ تنها اجرای فعال را برمی‌گرداند (خالی اگر اجرایی نیست).
func (m *runManager) activeSID() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	for sid := range m.runs {
		return sid
	}
	return ""
}

func (m *runManager) countFor(userID int64) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, r := range m.runs {
		if r.UserID == userID {
			n++
		}
	}
	return n
}

func (m *runManager) add(r *runCtl) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.runs[r.SID] = r
}

// setApproval کانال تأیید را برای نشست ثبت می‌کند (هنگام انتظار «شروع کن؟»).
func (m *runManager) setApproval(sid string, ch chan bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.approvals[sid] = ch
}

func (m *runManager) approval(sid string) chan bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.approvals[sid]
}

func (m *runManager) clearApproval(sid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.approvals, sid)
}

// remove اجرا را حذف و کانال done آن را می‌بندد (هر اجرا فقط یک‌بار).
func (m *runManager) remove(sid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[sid]; ok {
		close(r.done)
		delete(m.runs, sid)
	}
	delete(m.approvals, sid)
}

// abortAll همهٔ اجراها را لغو می‌کند (بعد از ری‌استارت سرور که اجراهای قبلی
// بی‌اعتبار می‌شوند).
func (m *runManager) abortAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, r := range m.runs {
		r.cancel()
		close(r.done)
		delete(m.runs, id)
	}
	for sid := range m.approvals {
		delete(m.approvals, sid)
	}
}
