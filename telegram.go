package main

import tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

// telegramAPI زیرمجموعهٔ API تلگرام است که ربات استفاده می‌کند. کانکریت آن
// *tgbotapi.BotAPI است؛ در تست‌ها می‌توان یک پیاده‌سازی fake به ربات داد تا
// بدون شبکه، رفتار (چه پیامی به چه چتی فرستاده می‌شود) را بررسی کرد.
type telegramAPI interface {
	Send(c tgbotapi.Chattable) (tgbotapi.Message, error)
	Request(c tgbotapi.Chattable) (*tgbotapi.APIResponse, error)
	GetFile(c tgbotapi.FileConfig) (tgbotapi.File, error)
}

// اطمینان از اینکه کلاینت واقعی اینترفیس را پیاده می‌کند
var _ telegramAPI = (*tgbotapi.BotAPI)(nil)
