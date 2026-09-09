// Package statefile ذخیره‌سازی اتمیک و debounceشدهٔ داده‌های JSON روی دیسک است.
// با نوشتن به فایل موقت و بعد rename، هیچ‌وقت فایل اصلی نیمه‌نوشته/خراب نمی‌شود.
package statefile

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"
)

// WriteAtomic داده را با یک عملیات اتمیک می‌نویسد: اول فایل موقت، بعد rename.
// اگر فرآیند وسط نوشتن از بین برود، فایل اصلی یا نسخهٔ قبلی سالم است.
func WriteAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	tmp := path + ".tmp"
	if dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}
	}
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Saver نوشتن‌های مکرر را جمع می‌کند (debounce) و در یک goroutine واحد با
// نوشتن اتمیک روی دیسک می‌برد تا هم از خرابی فایل جلوگیری شود و هم قفل
// ساختار داده برای مدت طولانی بسته نماند. تغییرات بعد از delay مشخصی نوشته
// می‌شوند و هنگام Close به‌صورت قطعی فلاش می‌شوند.
type Saver struct {
	path  string
	snap  func() ([]byte, error) // عکس لحظه‌ای از داده برای نوشتن
	delay time.Duration

	startOnce sync.Once
	stopOnce  sync.Once
	req       chan req
	quit      chan struct{}
	wg        sync.WaitGroup

	dirty   atomic.Bool
	lastErr atomic.Value // *saveErr

	onErr func(error) // در صورت خطا (اختیاری) برای لاگ
}

// saveErr خطای آخرین نوشتن را نگه می‌دارد (atomic.Value نمی‌تواند nil خام ذخیره کند).
type saveErr struct{ err error }

type req struct {
	flush bool
	ack   chan error
}

// NewSaver یک ذخیره‌کننده می‌سازد. snap در goroutine خودِ ذخیره‌کننده صدا زده
// می‌شود و باید در صورت نیاز، ساختار دادهٔ زیرین را خودش قفل کند.
func NewSaver(path string, snap func() ([]byte, error)) *Saver {
	return &Saver{
		path:  path,
		snap:  snap,
		delay: 400 * time.Millisecond,
		req:   make(chan req, 2),
		quit:  make(chan struct{}),
	}
}

// SetErrorHandler یک تابع برای گزارش خطاهای نوشتن روی دیسک ثبت می‌کند.
func (s *Saver) SetErrorHandler(f func(error)) {
	s.onErr = f
}

// MarkDirty بعد از هر تغییر، ذخیرهٔ بعدی را برنامه‌ریزی می‌کند. اگر تغییری در
// حال انتظار باشد، فقط علامت را می‌زند (نوشتن بعدی همهٔ تغییرات را می‌گیرد).
func (s *Saver) MarkDirty() {
	s.startOnce.Do(func() {
		s.wg.Add(1)
		go s.loop()
	})
	if !s.dirty.Swap(true) {
		select {
		case s.req <- req{}:
		default:
		}
	}
}

// Flush تغییرات در انتظار را همین حالا (هم‌زمان) روی دیسک می‌نویسد.
func (s *Saver) Flush() error {
	s.startOnce.Do(func() {
		s.wg.Add(1)
		go s.loop()
	})
	ack := make(chan error, 1)
	s.req <- req{flush: true, ack: ack}
	return <-ack
}

// Close ذخیره‌کننده را متوقف و تغییرات باقی‌مانده را به‌صورت قطعی فلاش می‌کند.
// بعد از Close نباید MarkDirty صدا زده شود.
func (s *Saver) Close() error {
	err := s.Flush()
	s.stopOnce.Do(func() {
		s.startOnce.Do(func() {
			s.wg.Add(1)
			go s.loop()
		})
		close(s.quit)
		s.wg.Wait()
	})
	return err
}

func (s *Saver) loop() {
	defer s.wg.Done()
	timer := time.NewTimer(time.Hour)
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	for {
		select {
		case r := <-s.req:
			if r.flush {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				s.writeIfDirty()
				r.ack <- s.getErr()
			} else {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
				timer.Reset(s.delay)
			}
		case <-timer.C:
			s.writeIfDirty()
		case <-s.quit:
			if !timer.Stop() {
				select {
				case <-timer.C:
				default:
				}
			}
			s.writeIfDirty()
			return
		}
	}
}

func (s *Saver) writeIfDirty() {
	if !s.dirty.Swap(false) {
		return
	}
	data, err := s.snap()
	if err != nil {
		s.fail(err)
		return
	}
	if err := WriteAtomic(s.path, data); err != nil {
		s.fail(err)
		return
	}
	s.lastErr.Store(&saveErr{})
}

func (s *Saver) fail(err error) {
	s.lastErr.Store(&saveErr{err})
	if s.onErr != nil {
		s.onErr(err)
	}
}

func (s *Saver) getErr() error {
	if v := s.lastErr.Load(); v != nil {
		return v.(*saveErr).err
	}
	return nil
}
