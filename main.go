package main

import (
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))
	slog.SetDefault(logger)

	cfg, err := loadConfig()
	if err != nil {
		slog.Error("بارگذاری تنظیمات ناموفق بود", "error", err)
		os.Exit(1)
	}
	api, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		slog.Error("اتصال به تلگرام ناموفق بود", "error", err)
		os.Exit(1)
	}
	slog.Info("ربات متصل شد", "bot", api.Self.UserName)

	bot := newBot(cfg, api)
	if err := bot.loadStates(); err != nil {
		slog.Warn("بارگذاری state ناموفق بود", "error", err)
	}
	// نرخ دلار→تومان: یک بار در شروع و بعد هر ۲۴ ساعت به‌روزرسانی می‌شود
	go bot.fx.keepWarm()
	// پیش‌بارگذاری کاتالوگ مدل‌ها تا اولین باز شدن تنظیمات معطل نکند
	go func() {
		if _, err := bot.cat.providers(); err != nil {
			slog.Warn("پیش‌بارگذاری کاتالوگ مدل‌ها ناموفق بود", "error", err)
		}
	}()

	u := tgbotapi.NewUpdate(0)
	u.Timeout = cfg.PollMs
	updates := api.GetUpdatesChan(u)

	stop := make(chan os.Signal, 1)
	signal.Notify(stop, os.Interrupt, syscall.SIGTERM)

	for {
		select {
		case upd := <-updates:
			bot.Handle(upd)
		case <-stop:
			slog.Info("سیگنال خروج دریافت شد؛ ذخیرهٔ state…")
			if err := bot.close(); err != nil {
				slog.Warn("ذخیرهٔ نهایی state ناموفق بود", "error", err)
			}
			return
		}
	}
}
