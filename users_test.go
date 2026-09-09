package main

import (
	"fmt"
	"path/filepath"
	"sync"
	"testing"

	"opencode-tg-bot/internal/occlient"
)

func TestUserStoreConcurrentActivate(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	u := newUserStore(path)
	const users = 8
	const per = 40
	var wg sync.WaitGroup
	for uid := int64(1); uid <= users; uid++ {
		wg.Add(1)
		go func(uid int64) {
			defer wg.Done()
			for j := 0; j < per; j++ {
				u.ensure(uid, 100+uid)
				u.activate(uid, fmt.Sprintf("ses_%d_%d", uid, j))
			}
		}(uid)
	}
	wg.Wait()
	if err := u.Close(); err != nil {
		t.Fatal(err)
	}

	// بازخوانی از دیسک و بررسی یکپارچگی
	u2 := newUserStore(path)
	if err := u2.load(); err != nil {
		t.Fatal(err)
	}
	defer u2.Close()
	for uid := int64(1); uid <= users; uid++ {
		st := u2.byID(uid)
		if st == nil || len(st.Sessions) != per {
			t.Fatalf("user %d sessions=%d want %d", uid, len(st.Sessions), per)
		}
		if st.SessionID != fmt.Sprintf("ses_%d_%d", uid, per-1) {
			t.Fatalf("user %d active=%q", uid, st.SessionID)
		}
	}
}

func TestUserStoreRenameKeepsManualAcrossSync(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	u := newUserStore(path)
	defer u.Close()
	u.ensure(1, 10)
	u.activate(1, "ses_a")
	u.activate(1, "ses_b")
	u.rename(1, "ses_b", "کار اصلی")

	list := []struct{ id string }{{"ses_b"}, {"ses_c"}}
	srv := make([]occlient.Session, 0, len(list))
	for _, e := range list {
		srv = append(srv, occlient.Session{ID: e.id})
	}
	u.syncLive(1, srv, nil)
	st := u.byID(1)
	if st.SessionID != "ses_b" {
		t.Fatalf("active=%q", st.SessionID)
	}
	if st.Manual["ses_b"] != true || st.Labels["ses_b"] != "کار اصلی" {
		t.Fatalf("manual name lost: %+v", st)
	}
	// نشست حذف‌شده از روی سرور باید از فهرست کاربر برود
	for _, sid := range st.Sessions {
		if sid == "ses_a" {
			t.Fatalf("ses_a should be removed, got %v", st.Sessions)
		}
	}
}
