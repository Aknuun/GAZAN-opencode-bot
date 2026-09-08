package main

import (
	"log"
	"os"
	"os/signal"
	"syscall"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

func main() {
	cfg, err := loadConfig()
	if err != nil {
		log.Fatalf("config: %v", err)
	}
	api, err := tgbotapi.NewBotAPI(cfg.Token)
	if err != nil {
		log.Fatalf("تلگرام: %v", err)
	}
	log.Printf("ربات با نام %s متصل شد", api.Self.UserName)

	bot := newBot(cfg, api)
	if err := bot.loadStates(); err != nil {
		log.Printf("هشدار: بارگذاری state ناموفق: %v", err)
	}

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
			log.Println("خروج…")
			return
		}
	}
}
