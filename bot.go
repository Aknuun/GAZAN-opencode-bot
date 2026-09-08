package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

const helpText = `ربات کنترل opencode روی سرور

ارسال پیام متنی / فایل => اجرای آن توسط opencode (در همان session)

دکمه‌های ثابت زیر پیام‌ها:
📊 وضعیت و هزینه — مبلغ و توکنِ session فعلی
⚙️ تنظیمات — تغییر agent، مدل/API و مدیریت سرور
⏹ توقف عملیات — توقف اجرای فعلی و خالی کردن صف

دستورات:
/new         شروع گفتگوی جدید (session تازه)
/use <id>    ادامهٔ یک session قبلی
/list        فهرست sessionهای اخیر
/status      جزئیات کامل وضعیت و هزینه
/agent       نمایش/تغییر agent فعلی
/queue       تعداد موارد در صف
/flush       پاک کردن صف
/cancel      توقف اجرای جاری
/help        این راهنما

حین اجرا پاسخ به‌صورت زنده به‌روز می‌شود؛ فرمان‌های جدید در صف می‌مانند.`

const (
	btnStatus    = "📊 وضعیت و هزینه"
	btnSettings  = "⚙️ تنظیمات"
	btnStop      = "⏹ توقف عملیات"
	maxFileBytes = 30 << 20
)

var cancelTag = "cancel_run"

type UserState struct {
	UserID    int64  `json:"user_id"`
	ChatID    int64  `json:"chat_id"`
	SessionID string `json:"session_id,omitempty"`
	Agent     string `json:"agent,omitempty"`
	Pending   string `json:"pending,omitempty"`
}

type queueItem struct {
	ChatID int64
	Prompt string
}

type costInfo struct {
	when  time.Time
	label string
}

type Bot struct {
	cfg        *Config
	oc         *OCClient
	api        *tgbotapi.BotAPI
	mu         sync.Mutex
	states     map[int64]*UserState
	busy       map[int64]bool
	cancels    map[int64]context.CancelFunc
	queues     map[int64][]queueItem
	processing map[int64]bool
	costCache  map[int64]costInfo
}

func newBot(cfg *Config, api *tgbotapi.BotAPI) *Bot {
	return &Bot{
		cfg:        cfg,
		oc:         newOCClient(cfg.BaseURL, cfg.Agent),
		api:        api,
		states:     map[int64]*UserState{},
		busy:       map[int64]bool{},
		cancels:    map[int64]context.CancelFunc{},
		queues:     map[int64][]queueItem{},
		processing: map[int64]bool{},
		costCache:  map[int64]costInfo{},
	}
}

func (b *Bot) loadStates() error {
	data, err := os.ReadFile(b.cfg.StateFile)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	return json.Unmarshal(data, &b.states)
}

func (b *Bot) saveStates() error {
	data, err := json.Marshal(b.states)
	if err != nil {
		return err
	}
	return os.WriteFile(b.cfg.StateFile, data, 0o600)
}

func (b *Bot) allowed(userID int64) bool {
	return b.cfg.Allowed[userID]
}

func (b *Bot) stateFor(userID, chatID int64) *UserState {
	b.mu.Lock()
	defer b.mu.Unlock()
	st, ok := b.states[userID]
	if !ok {
		st = &UserState{UserID: userID, ChatID: chatID}
		b.states[userID] = st
		b.saveStates()
	}
	st.ChatID = chatID
	return st
}

func (b *Bot) snapshot(userID int64) (string, string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.states[userID]
	if st == nil {
		return "", ""
	}
	return st.SessionID, st.Agent
}

func (b *Bot) setSession(userID int64, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if st := b.states[userID]; st != nil {
		st.SessionID = id
		b.saveStates()
	}
}

func (b *Bot) setAgentValue(userID int64, agent string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if st := b.states[userID]; st != nil {
		st.Agent = agent
		b.saveStates()
	}
}

func (b *Bot) ensureSession(userID, chatID int64) (string, error) {
	if sid, _ := b.snapshot(userID); sid != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := b.oc.GetSession(ctx, sid); err == nil {
			return sid, nil
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := b.oc.CreateSession(ctx)
	if err != nil {
		return "", fmt.Errorf("ساخت session ممکن نشد: %v", err)
	}
	b.setSession(userID, s.ID)
	b.stateFor(userID, chatID)
	return s.ID, nil
}

func (b *Bot) agentFor(userID int64) string {
	_, agent := b.snapshot(userID)
	return agent
}

func (b *Bot) userForChat(chatID int64) int64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	for uid, st := range b.states {
		if st.ChatID == chatID {
			return uid
		}
	}
	return 0
}

func (b *Bot) costLabel(userID int64) string {
	sessionID, _ := b.snapshot(userID)
	if sessionID == "" {
		return btnStatus
	}
	b.mu.Lock()
	if c, ok := b.costCache[userID]; ok && time.Since(c.when) < 4*time.Second {
		b.mu.Unlock()
		return c.label
	}
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	s, err := b.oc.GetSession(ctx, sessionID)
	label := btnStatus
	if err == nil {
		total := s.Tokens.Input + s.Tokens.Output
		if s.Cost == 0 && total == 0 {
			label = "📊 هزینه $0 · بدون مصرف"
		} else {
			label = fmt.Sprintf("📊 $%.4f · %s توکن", s.Cost, abbrev(total))
		}
	}
	b.mu.Lock()
	b.costCache[userID] = costInfo{when: time.Now(), label: label}
	b.mu.Unlock()
	return label
}

func abbrev(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1e6)
	case n >= 1000:
		return fmt.Sprintf("%.1fk", float64(n)/1e3)
	default:
		return strconv.Itoa(n)
	}
}

func (b *Bot) replyKeyboard(chatID int64) tgbotapi.ReplyKeyboardMarkup {
	label := btnStatus
	if uid := b.userForChat(chatID); uid != 0 {
		label = b.costLabel(uid)
	}
	return tgbotapi.ReplyKeyboardMarkup{
		ResizeKeyboard: true,
		Keyboard: [][]tgbotapi.KeyboardButton{
			{
				{Text: label},
				{Text: btnSettings},
				{Text: btnStop},
			},
		},
	}
}

func (b *Bot) cancelKeyboard() *tgbotapi.InlineKeyboardMarkup {
	return &tgbotapi.InlineKeyboardMarkup{
		InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{{
			{Text: "⏹ توقف", CallbackData: &cancelTag},
		}},
	}
}

func (b *Bot) send(chatID int64, text string) (int, error) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = b.replyKeyboard(chatID)
	m, err := b.api.Send(msg)
	if err != nil {
		return 0, err
	}
	return m.MessageID, nil
}

func (b *Bot) sendProgress(chatID int64, text string) (int, error) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = b.cancelKeyboard()
	m, err := b.api.Send(msg)
	if err != nil {
		return 0, err
	}
	return m.MessageID, nil
}

func (b *Bot) edit(chatID int64, msgID int, text string) {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	b.api.Send(edit)
}

func (b *Bot) removeInline(chatID int64, msgID int) {
	rm := tgbotapi.NewEditMessageReplyMarkup(chatID, msgID, tgbotapi.InlineKeyboardMarkup{})
	b.api.Send(rm)
}

func (b *Bot) sendChunks(chatID int64, text string, progressMsgID int) {
	const max = 4000
	runes := []rune(text)
	if len(runes) == 0 {
		runes = []rune("(پاسخی نبود)")
	}
	if len(runes) <= max {
		if progressMsgID != 0 {
			b.edit(chatID, progressMsgID, text)
			b.removeInline(chatID, progressMsgID)
			return
		}
		b.send(chatID, text)
		return
	}
	var parts []string
	for len(runes) > max {
		parts = append(parts, string(runes[:max]))
		runes = runes[max:]
	}
	parts = append(parts, string(runes))
	if progressMsgID != 0 {
		b.edit(chatID, progressMsgID, "پاسخ کامل شد، در حال ارسال…")
	}
	b.send(chatID, parts[0])
	for _, p := range parts[1:] {
		b.send(chatID, p)
	}
	if progressMsgID != 0 {
		del := tgbotapi.NewDeleteMessage(chatID, progressMsgID)
		b.api.Send(del)
	}
}

func (b *Bot) Handle(upd tgbotapi.Update) {
	if upd.CallbackQuery != nil {
		b.handleCallback(upd.CallbackQuery)
		return
	}
	if upd.Message == nil {
		return
	}
	userID := upd.Message.From.ID
	chatID := upd.Message.Chat.ID
	if !b.allowed(userID) {
		b.send(chatID, "شما مجاز به استفاده از این ربات نیستید.")
		return
	}
	b.stateFor(userID, chatID)

	if upd.Message.Document != nil {
		b.handleFile(userID, chatID, upd.Message.Document.FileID, upd.Message.Document.FileName, upd.Message.Caption, false)
		return
	}
	if len(upd.Message.Photo) > 0 {
		p := upd.Message.Photo[len(upd.Message.Photo)-1]
		b.handleFile(userID, chatID, p.FileID, "photo.jpg", upd.Message.Caption, true)
		return
	}
	if upd.Message.Text == "" {
		return
	}
	text := strings.TrimSpace(upd.Message.Text)
	if strings.HasPrefix(text, "📊") {
		b.showStatus(userID, chatID)
		return
	}
	switch text {
	case btnSettings:
		b.openSettings(userID, chatID)
		return
	case btnStop:
		b.cancelRun(userID, chatID)
		return
	}
	if st := b.stateFor(userID, chatID); st.Pending != "" {
		if strings.HasPrefix(text, "/") {
			b.clearPending(st)
			b.handleCommand(upd, text)
			return
		}
		b.handlePending(userID, chatID, st.Pending, text)
		return
	}
	if strings.HasPrefix(text, "/") {
		b.handleCommand(upd, text)
		return
	}
	b.submitPrompt(userID, chatID, text)
}

func (b *Bot) handleCallback(cq *tgbotapi.CallbackQuery) {
	userID := cq.From.ID
	chatID := cq.Message.Chat.ID
	if !b.allowed(userID) {
		return
	}
	b.api.Request(tgbotapi.NewCallback(cq.ID, ""))
	if strings.HasPrefix(cq.Data, "s:") {
		b.onSettingsCallback(userID, chatID, cq.Message.MessageID, cq.Data)
		return
	}
	if cq.Data == cancelTag {
		b.cancelRun(userID, chatID)
	}
}

func (b *Bot) setAgent(userID, chatID int64, agent string) {
	b.setAgentValue(userID, agent)
	b.send(chatID, "agent فعال: "+agent)
}

func (b *Bot) handleFile(userID, chatID int64, fileID, fileName, caption string, isPhoto bool) {
	path, err := b.download(fileID, fileName)
	if err != nil {
		b.send(chatID, "دریافت فایل ناموفق بود: "+err.Error())
		return
	}
	prompt := strings.TrimSpace(caption)
	if prompt == "" {
		if isPhoto {
			prompt = "این تصویر پیوست‌شده را تحلیل کن و نتیجه را گزارش بده."
		} else {
			prompt = "محتوای این فایل پیوست‌شده را بررسی کن و خلاصه یا پاسخ مناسب بده."
		}
	}
	b.submitPrompt(userID, chatID, prompt+"\n\nفایل پیوست: "+path)
}

func (b *Bot) download(fileID, fileName string) (string, error) {
	file, err := b.api.GetFile(tgbotapi.FileConfig{FileID: fileID})
	if err != nil {
		return "", err
	}
	if file.FileSize > maxFileBytes {
		return "", fmt.Errorf("فایل بزرگ‌تر از حد مجاز (%d مگابایت) است", maxFileBytes>>20)
	}
	url := "https://api.telegram.org/file/bot" + b.cfg.Token + "/" + file.FilePath
	resp, err := http.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return "", fmt.Errorf("دریافت فایل از تلگرام: %s", resp.Status)
	}
	dir := filepath.Join(filepath.Dir(b.cfg.StateFile), "downloads", "upload")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := filepath.Base(fileName)
	if name == "." || name == "/" || name == "" {
		name = "file"
	}
	dest := filepath.Join(dir, name)
	out, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	defer out.Close()
	if _, err := io.Copy(out, resp.Body); err != nil {
		return "", err
	}
	return dest, nil
}

func (b *Bot) handleCommand(upd tgbotapi.Update, text string) {
	userID := upd.Message.From.ID
	chatID := upd.Message.Chat.ID
	cmd := text
	arg := ""
	if i := strings.IndexAny(cmd, " \n"); i >= 0 {
		cmd, arg = cmd[:i], strings.TrimSpace(cmd[i+1:])
	}
	switch cmd {
	case "/start", "/help":
		b.send(chatID, helpText)
	case "/new":
		b.setSession(userID, "")
		sid, err := b.ensureSession(userID, chatID)
		if err != nil {
			b.send(chatID, err.Error())
			return
		}
		b.send(chatID, "گفتگوی جدید شروع شد.\n"+sid)
	case "/use":
		if arg == "" {
			b.send(chatID, "استفاده: /use <session id>")
			return
		}
		b.setSession(userID, arg)
		b.send(chatID, "session انتخاب شد: "+arg)
	case "/list":
		b.listSessions(chatID)
	case "/status":
		b.showStatus(userID, chatID)
	case "/agent":
		cur := b.agentFor(userID)
		if arg != "" {
			b.setAgent(userID, chatID, arg)
			return
		}
		b.send(chatID, "agent فعلی: "+cur+"\nبا دکمه‌های 🛠/🤖/🗂 عوضش کن.")
	case "/queue":
		b.showQueue(userID, chatID)
	case "/flush":
		b.flushQueue(userID, chatID)
	case "/cancel":
		b.cancelRun(userID, chatID)
	default:
		b.send(chatID, "دستور ناشناخته. برای راهنما: /help")
	}
}

func (b *Bot) listSessions(chatID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	sessions, err := b.oc.ListSessions(ctx, 8)
	if err != nil {
		b.send(chatID, "خطا در دریافت فهرست: "+err.Error())
		return
	}
	var sb strings.Builder
	sb.WriteString("آخرین sessionها:\n")
	for _, s := range sessions {
		title := strings.TrimSpace(s.Title)
		if len([]rune(title)) > 40 {
			title = string([]rune(title)[:40]) + "…"
		}
		fmt.Fprintf(&sb, "\n%s\n  %s\n  هزینه: $%.4f", s.ID, title, s.Cost)
	}
	b.send(chatID, sb.String())
}

func (b *Bot) showStatus(userID, chatID int64) {
	sessionID, agent := b.snapshot(userID)
	if sessionID == "" {
		b.send(chatID, "هنوز session‌ای ساخته نشده. /new بزن یا یک متن بفرست.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := b.oc.GetSession(ctx, sessionID)
	if err != nil {
		b.send(chatID, "خطا: "+err.Error())
		return
	}
	if agent == "" {
		agent = b.cfg.Agent
	}
	msg := fmt.Sprintf("session: %s\n", s.ID)
	if t := strings.TrimSpace(s.Title); t != "" {
		msg += "عنوان: " + t + "\n"
	}
	msg += fmt.Sprintf("agent: %s\n", agent)
	msg += fmt.Sprintf("مدل: %s\n", s.ModelID)
	msg += fmt.Sprintf("توکن ورودی: %d\n", s.Tokens.Input)
	msg += fmt.Sprintf("توکن خروجی: %d\n", s.Tokens.Output)
	msg += fmt.Sprintf("جمع توکن: %s\n", abbrev(s.Tokens.Input+s.Tokens.Output))
	msg += fmt.Sprintf("هزینه: $%.4f", s.Cost)
	b.send(chatID, msg)
}

func (b *Bot) showQueue(userID, chatID int64) {
	b.mu.Lock()
	n := len(b.queues[userID])
	busy := b.busy[userID]
	b.mu.Unlock()
	if busy {
		b.send(chatID, "یک مورد در حال اجراست و "+strconv.Itoa(n)+" مورد در صف است.")
		return
	}
	if n == 0 {
		b.send(chatID, "صف خالی است.")
		return
	}
	b.send(chatID, strconv.Itoa(n)+" مورد در صف است.")
}

func (b *Bot) flushQueue(userID, chatID int64) {
	b.mu.Lock()
	n := len(b.queues[userID])
	b.queues[userID] = nil
	b.mu.Unlock()
	if n == 0 {
		b.send(chatID, "صف از قبل خالی بود.")
		return
	}
	b.send(chatID, fmt.Sprintf("%d مورد صف پاک شد.", n))
}

func (b *Bot) cancelRun(userID, chatID int64) {
	sessionID, _ := b.snapshot(userID)
	b.mu.Lock()
	busy := b.busy[userID]
	var c context.CancelFunc
	if busy {
		c = b.cancels[userID]
	}
	pending := len(b.queues[userID])
	b.queues[userID] = nil
	b.mu.Unlock()

	if !busy && pending == 0 {
		b.send(chatID, "هیچ اجرایی در جریان نیست.")
		return
	}
	if c != nil {
		c()
	}
	if sessionID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		b.oc.Abort(ctx, sessionID)
	}
	msg := "اجرا متوقف شد."
	if pending > 0 {
		msg += fmt.Sprintf(" %d مورد صف هم حذف شد.", pending)
	}
	b.send(chatID, msg)
}

func (b *Bot) setBusy(userID int64, busy bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.busy[userID] = busy
	if !busy {
		delete(b.cancels, userID)
	}
}

func (b *Bot) isBusy(userID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.busy[userID]
}

func (b *Bot) submitPrompt(userID, chatID int64, prompt string) {
	if !b.ocReady() {
		b.send(chatID, b.ocSetupHint())
		b.openSettings(userID, chatID)
		return
	}
	b.mu.Lock()
	if b.processing[userID] {
		b.queues[userID] = append(b.queues[userID], queueItem{ChatID: chatID, Prompt: prompt})
		n := len(b.queues[userID])
		b.mu.Unlock()
		b.send(chatID, fmt.Sprintf("یک کار در جریان است؛ فرمان در صف قرار گرفت (موقعیت %d).", n))
		return
	}
	b.processing[userID] = true
	b.queues[userID] = append(b.queues[userID], queueItem{ChatID: chatID, Prompt: prompt})
	b.mu.Unlock()
	go b.processQueue(userID)
}

func (b *Bot) processQueue(userID int64) {
	for {
		b.mu.Lock()
		q := b.queues[userID]
		if len(q) == 0 {
			b.processing[userID] = false
			b.mu.Unlock()
			return
		}
		item := q[0]
		b.queues[userID] = q[1:]
		b.mu.Unlock()
		b.runPrompt(userID, item.ChatID, item.Prompt)
	}
}

func (b *Bot) runPrompt(userID, chatID int64, prompt string) {
	b.setBusy(userID, true)
	defer b.setBusy(userID, false)

	sessionID, agent := b.snapshot(userID)
	if sessionID == "" {
		var err error
		sessionID, err = b.ensureSession(userID, chatID)
		if err != nil {
			b.send(chatID, err.Error())
			return
		}
		_, agent = b.snapshot(userID)
	}

	progressMsg, err := b.sendProgress(chatID, "در حال انجام…")
	if err != nil {
		return
	}

	ctx, cancel := context.WithCancel(context.Background())
	b.mu.Lock()
	b.cancels[userID] = cancel
	b.mu.Unlock()
	defer cancel()

	if err := b.oc.PromptAsync(ctx, sessionID, prompt, agent); err != nil {
		b.edit(chatID, progressMsg, "ارسال دستور ناموفق بود: "+err.Error())
		return
	}

	final := b.poll(ctx, userID, chatID, sessionID, progressMsg)
	b.sendChunks(chatID, final, progressMsg)
}

func (b *Bot) poll(ctx context.Context, userID int64, chatID int64, sessionID string, progressMsg int) string {
	ticker := time.NewTicker(1500 * time.Millisecond)
	defer ticker.Stop()
	start := time.Now()
	lastShown := ""
	lastEdit := time.Time{}
	errCount := 0
	for {
		select {
		case <-ctx.Done():
			return "⛔ متوقف شد."
		case now := <-ticker.C:
			msg, err := b.oc.LastMessage(ctx, sessionID)
			if err != nil {
				errCount++
				if errCount > 5 {
					return "⚠️ ارتباط با opencode قطع شد: " + err.Error()
				}
				continue
			}
			errCount = 0

			curText := ""
			if msg != nil && msg.Info.Role == "assistant" {
				curText = joinText(msg)
				if isFinalFinish(msg.Info.Finish) {
					if curText == "" {
						curText = "⚠️ پاسخی دریافت نشد."
					}
					return curText
				}
				if qs := pendingQuestions(msg); qs != "" {
					b.oc.Abort(ctx, sessionID)
					if curText == "" {
						curText = "⚠️ پاسخ ناقص تولید شد."
					}
					return curText + "\n\n— — —\n" + qs + "\n\n(ربات تلگرام نمی‌تواند به سؤال‌های ابزار پاسخ دهد، برای همین این اجرا متوقف شد. پاسخ‌ها را مستقیم بنویس تا ادامه بدهد.)"
				}
			}

			interval := 900 * time.Millisecond
			show := preview(curText)
			if curText == "" {
				interval = 2 * time.Second
				label := ""
				if msg != nil && msg.Info.Role == "assistant" {
					label = activityLabel(msg)
				}
				if label == "" {
					label = "🧠 در حال فکر کردن…"
				}
				show = label + "\n⏳ " + durText(int(now.Sub(start).Seconds()))
			}
			if show != lastShown && now.Sub(lastEdit) >= interval {
				b.edit(chatID, progressMsg, show)
				lastShown = show
				lastEdit = now
			}
		}
	}
}

func isFinalFinish(finish string) bool {
	switch finish {
	case "", "pending", "tool-calls":
		return false
	}
	return true
}

func pendingQuestions(msg *OCMessage) string {
	for _, p := range msg.Parts {
		if p.Type != "tool" || p.Tool != "question" || p.State == nil {
			continue
		}
		if p.State.Status == "completed" || p.State.Status == "error" {
			continue
		}
		var sb strings.Builder
		sb.WriteString("🤔 مدل در پایانِ پاسخ این سؤال‌ها را پرسید:")
		if qs, ok := p.State.Input["questions"].([]any); ok {
			for i, raw := range qs {
				q, ok := raw.(map[string]any)
				if !ok {
					continue
				}
				sb.WriteString("\n\n")
				fmt.Fprintf(&sb, "%d) %s", i+1, anyStr(q["question"]))
				if h := anyStr(q["header"]); h != "" {
					sb.WriteString("\n   [" + h + "]")
				}
				if opts, ok := q["options"].([]any); ok {
					for _, oraw := range opts {
						om, ok := oraw.(map[string]any)
						if !ok {
							continue
						}
						opt := anyStr(om["label"])
						if d := anyStr(om["description"]); d != "" {
							opt += " — " + d
						}
						sb.WriteString("\n   • " + opt)
					}
				}
			}
		}
		return sb.String()
	}
	return ""
}

func anyStr(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}

func joinText(msg *OCMessage) string {
	var sb strings.Builder
	for _, p := range msg.Parts {
		if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
			sb.WriteString(p.Text)
			sb.WriteString("\n")
		}
	}
	return strings.TrimSpace(sb.String())
}

func preview(text string) string {
	if text == "" {
		return "🧠 در حال فکر کردن…"
	}
	r := []rune(text)
	const max = 3500
	if len(r) > max {
		return string(r[:max]) + "\n… (در حال تولید)"
	}
	return text + "\n… (در حال تولید)"
}

func activityLabel(msg *OCMessage) string {
	reasoning := ""
	lastTool := ""
	for i := len(msg.Parts) - 1; i >= 0; i-- {
		p := msg.Parts[i]
		switch p.Type {
		case "reasoning":
			if reasoning == "" {
				reasoning = collapse(p.Text)
			}
		case "tool":
			if label := toolLabel(p); label != "" && lastTool == "" {
				lastTool = label
			}
		}
	}
	var lines []string
	if reasoning != "" {
		lines = append(lines, "🤔 "+clipTail(reasoning, 180))
	}
	if lastTool != "" {
		lines = append(lines, lastTool)
	}
	return strings.Join(lines, "\n")
}

func toolLabel(p OCPart) string {
	name := p.Tool
	if name == "" {
		return ""
	}
	status := ""
	target := ""
	if p.State != nil {
		status = p.State.Status
		for _, k := range []string{"command", "filePath", "file_path", "path", "query"} {
			if v, ok := p.State.Input[k].(string); ok && strings.TrimSpace(v) != "" {
				target = v
				break
			}
		}
		if target == "" {
			target = p.State.Title
		}
	}
	target = clipHead(collapse(target), 90)
	icon := "🔄"
	switch status {
	case "completed":
		icon = "✅"
	case "error":
		icon = "❌"
	}
	if target != "" {
		return fmt.Sprintf("%s %s: %s", icon, name, target)
	}
	return fmt.Sprintf("%s %s", icon, name)
}

func collapse(s string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(s)), " ")
}

func clipHead(s string, n int) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n]) + "…"
}

func clipTail(s string, n int) string {
	if s == "" {
		return ""
	}
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return "…" + string(r[len(r)-n:])
}

func durText(secs int) string {
	if secs < 60 {
		return strconv.Itoa(secs) + " ثانیه"
	}
	return fmt.Sprintf("%d دقیقه و %d ثانیه", secs/60, secs%60)
}
