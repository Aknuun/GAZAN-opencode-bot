package main

import "sync"

// runManager مالک اجراهای هم‌زمان نشست‌هاست و آن‌ها را زیر قفل خودش نگه می‌دارد.
type runManager struct {
	mu   sync.Mutex
	runs map[string]*runCtl // کلید = نشست opencode
}

func newRunManager() *runManager {
	return &runManager{runs: map[string]*runCtl{}}
}

func (m *runManager) get(sid string) (*runCtl, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	r, ok := m.runs[sid]
	return r, ok
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

// remove اجرا را حذف و کانال done آن را می‌بندد (هر اجرا فقط یک‌بار).
func (m *runManager) remove(sid string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if r, ok := m.runs[sid]; ok {
		close(r.done)
		delete(m.runs, sid)
	}
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
}
