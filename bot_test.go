package main

import (
	"testing"
)

func TestFaNum(t *testing.T) {
	cases := map[int]string{0: "۰", 1: "۱", 9: "۹", 12: "۱۲", 100: "۱۰۰"}
	for in, want := range cases {
		if got := faNum(in); got != want {
			t.Fatalf("faNum(%d)=%q want %q", in, got, want)
		}
	}
}

func TestRenumberAutoLabels(t *testing.T) {
	st := &UserState{
		Sessions: []string{"a", "b", "c"},
		Labels:   map[string]string{"a": "قدیمی", "b": "نام دستی", "c": ""},
		Manual:   map[string]bool{"b": true},
	}
	renumberAutoLabels(st)
	if st.Labels["a"] != "۱" {
		t.Fatalf("a=%q", st.Labels["a"])
	}
	if st.Labels["b"] != "نام دستی" {
		t.Fatalf("b=%q", st.Labels["b"])
	}
	if st.Labels["c"] != "۳" {
		t.Fatalf("c=%q", st.Labels["c"])
	}
}

func TestRenumberAfterDelete(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{BaseURL: "http://127.0.0.1:1", StateFile: dir + "/state.json"}
	b := newBot(cfg, nil)
	b.states[1] = &UserState{
		UserID:    1,
		Sessions:  []string{"a", "b", "c"},
		Labels:    map[string]string{"a": "۱", "b": "۲", "c": "۳"},
		SessionID: "b",
	}
	b.deleteSession(1, "b")
	st := b.states[1]
	if st.Labels["a"] != "۱" || st.Labels["c"] != "۲" {
		t.Fatalf("labels=%v", st.Labels)
	}
	if st.SessionID != "" {
		t.Fatalf("active should be cleared")
	}
}

func TestSetSessionLabelMarksManual(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{BaseURL: "http://127.0.0.1:1", StateFile: dir + "/state.json"}
	b := newBot(cfg, nil)
	b.states[1] = &UserState{UserID: 1, Sessions: []string{"a"}}
	b.setSessionLabel(1, "a", "مدل جدید")
	st := b.states[1]
	if st.Labels["a"] != "مدل جدید" || !st.Manual["a"] {
		t.Fatalf("labels=%v manual=%v", st.Labels, st.Manual)
	}
	// شماره‌گذاری خودکار نباید روی نام دستی بنشیند
	renumberAutoLabels(st)
	if st.Labels["a"] != "مدل جدید" {
		t.Fatalf("manual label overwritten: %q", st.Labels["a"])
	}
}
