package statefile

import (
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

func TestWriteAtomic(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	if err := WriteAtomic(path, []byte("one")); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "one" {
		t.Fatalf("got %q", b)
	}
	if err := WriteAtomic(path, []byte("two")); err != nil {
		t.Fatal(err)
	}
	b, _ = os.ReadFile(path)
	if string(b) != "two" {
		t.Fatalf("got %q", b)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Fatalf("tmp file should be cleaned up")
	}
}

func TestSaverDebounceAndFlush(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	var v atomic.Int64
	s := NewSaver(path, func() ([]byte, error) {
		return []byte("v=" + itoa(v.Load())), nil
	})
	defer s.Close()

	s.MarkDirty()
	v.Store(1)
	s.MarkDirty()
	v.Store(2)

	// هنوز بعد از delay نباید نوشته شده باشد
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("should not be flushed before delay")
	}
	// MarkDirty نزدیک هم باید فقط یک نوشتن منجر شود (debounce)
	time.Sleep(700 * time.Millisecond)
	b, _ := os.ReadFile(path)
	if string(b) != "v=2" {
		t.Fatalf("debounced value=%q want v=2", b)
	}
}

func TestSaverFlushNow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	var v atomic.Int64
	s := NewSaver(path, func() ([]byte, error) {
		return []byte(itoa(v.Load())), nil
	})
	defer s.Close()

	v.Store(42)
	s.MarkDirty()
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "42" {
		t.Fatalf("flush value=%q", b)
	}
}

func TestSaverSnapshotErrorKeepsDirty(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	var fail atomic.Bool
	var v atomic.Int64
	s := NewSaver(path, func() ([]byte, error) {
		if fail.Load() {
			return nil, os.ErrPermission
		}
		return []byte(itoa(v.Load())), nil
	})
	defer s.Close()

	fail.Store(true)
	s.MarkDirty()
	if err := s.Flush(); err == nil {
		t.Fatal("expected flush error")
	}
	fail.Store(false)
	v.Store(7)
	s.MarkDirty()
	if err := s.Flush(); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != "7" {
		t.Fatalf("value after retry=%q", b)
	}
}

func itoa(n int64) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
