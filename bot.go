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

ارسال پیام متنی / فایل => اجرا روی نشستِ فعال فعلی

دکمه‌های ثابت زیر پیام‌ها:
📊 وضعیت و هزینه — وضعیت نشست فعال
⚙️ تنظیمات — مدل، کلید API و agent
🗂 نشست‌ها — مدیریت نشست‌ها (ساخت/تعویض/توقف)

نشست‌ها می‌توانند هم‌زمان اجرا شوند؛ تعویض نشست، اجرای در جریان را متوقف نمی‌کند.

دستورات:
/new         ساخت نشست جدید و رفتن به آن
/use <id>    رفتن به نشست مشخص
/sessions    باز کردن مدیریت نشست‌ها
/list        فهرست نشست‌های اخیر سرور
/status      جزئیات نشست فعال
/agent       نمایش/تغییر agent
/cancel      توقف اجرای نشست فعال
/help        این راهنما

حین اجرا پاسخ به‌صورت زنده به‌روز می‌شود؛ روی پیام «در حال انجام» دکمهٔ ⏹ توقف همان نشست است.`

const (
	btnStatus    = "📊 وضعیت و هزینه"
	btnSettings  = "⚙️ تنظیمات"
	btnSessions  = "🗂 نشست‌ها"
	maxFileBytes = 30 << 20

	// حداکثر اجرای هم‌زمان برای هر کاربر
	maxConcurrentRuns = 6
)

type UserState struct {
	UserID    int64             `json:"user_id"`
	ChatID    int64             `json:"chat_id"`
	SessionID string            `json:"session_id,omitempty"`
	Sessions  []string          `json:"sessions,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"` // نام فارسی نشست‌ها
	Agent     string            `json:"agent,omitempty"`
	Pending   string            `json:"pending,omitempty"`
}

// runCtl کنترل اجرای هم‌زمان یک نشست
type runCtl struct {
	SID     string
	UserID  int64
	ChatID  int64
	cancel  context.CancelFunc
	done    chan struct{}
	started time.Time
}

type Bot struct {
	cfg    *Config
	oc     *OCClient
	api    *tgbotapi.BotAPI
	mu     sync.Mutex
	states map[int64]*UserState
	runs   map[string]*runCtl // کلید = نشست opencode
	cost   map[int64]costInfo
}

type costInfo struct {
	when  time.Time
	label string
}

func newBot(cfg *Config, api *tgbotapi.BotAPI) *Bot {
	return &Bot{
		cfg:    cfg,
		oc:     newOCClient(cfg.BaseURL, cfg.Agent),
		api:    api,
		states: map[int64]*UserState{},
		runs:   map[string]*runCtl{},
		cost:   map[int64]costInfo{},
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
	if err := json.Unmarshal(data, &b.states); err != nil {
		return err
	}
	b.normalizeStates()
	return nil
}

// normalizeStates برچسب‌های پیش‌فرض برای نشست‌های قدیمی/بدون نام می‌سازد
func (b *Bot) normalizeStates() {
	for _, st := range b.states {
		if st.Labels == nil {
			st.Labels = map[string]string{}
		}
		// مطمئن شو نشست فعال در لیست هست
		if st.SessionID != "" {
			st.Sessions = addSessionID(st.Sessions, st.SessionID)
		}
		for i, sid := range st.Sessions {
			if st.Labels[sid] == "" {
				st.Labels[sid] = defaultSessionName(i + 1)
			}
		}
	}
	b.saveStates()
}

func defaultSessionName(n int) string {
	return fmt.Sprintf("نشست %d", n)
}

// sessionLabel نام نمایشی نشست
func (b *Bot) sessionLabel(st *UserState, sid string) string {
	if st.Labels != nil {
		if l := st.Labels[sid]; l != "" {
			return l
		}
	}
	for i, s := range st.Sessions {
		if s == sid {
			return defaultSessionName(i + 1)
		}
	}
	return shortSID(sid)
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

func (b *Bot) stateOf(userID int64) *UserState {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.states[userID]
}

func (b *Bot) setSession(userID int64, id string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.states[userID]
	if st == nil {
		return
	}
	had := false
	for _, s := range st.Sessions {
		if s == id {
			had = true
			break
		}
	}
	if !had {
		st.Sessions = append(st.Sessions, id)
	}
	st.SessionID = id
	if st.Labels == nil {
		st.Labels = map[string]string{}
	}
	if st.Labels[id] == "" {
		st.Labels[id] = defaultSessionName(len(st.Sessions))
	}
	b.saveStates()
}

func addSessionID(list []string, id string) []string {
	for _, s := range list {
		if s == id {
			return list
		}
	}
	return append(list, id)
}

// setSessionLabel نام (عنوان موضوعی) نشست را به‌روز می‌کند
func (b *Bot) setSessionLabel(userID int64, sid, label string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.states[userID]
	if st == nil {
		return
	}
	if st.Labels == nil {
		st.Labels = map[string]string{}
	}
	st.Labels[sid] = label
	b.saveStates()
}

// deleteSession حذف نشست از فهرست کاربر (+ توقف اجرا اگر در جریان باشد)
func (b *Bot) deleteSession(userID int64, sid string) {
	b.stopRun(sid)
	b.mu.Lock()
	defer b.mu.Unlock()
	st := b.states[userID]
	if st == nil {
		return
	}
	var keep []string
	for _, s := range st.Sessions {
		if s != sid {
			keep = append(keep, s)
		}
	}
	st.Sessions = keep
	if st.Labels != nil {
		delete(st.Labels, sid)
	}
	if st.SessionID == sid {
		st.SessionID = ""
	}
	b.saveStates()
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
	if st := b.stateOf(userID); st != nil && st.SessionID != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if _, err := b.oc.GetSession(ctx, st.SessionID); err == nil {
			return st.SessionID, nil
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
	if st := b.stateOf(userID); st != nil && st.Agent != "" {
		return st.Agent
	}
	if b.cfg != nil {
		return b.cfg.Agent
	}
	return "build"
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
	st := b.stateOf(userID)
	if st == nil || st.SessionID == "" {
		return btnStatus
	}
	b.mu.Lock()
	if c, ok := b.cost[userID]; ok && time.Since(c.when) < 4*time.Second {
		b.mu.Unlock()
		return c.label
	}
	b.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	s, err := b.oc.GetSession(ctx, st.SessionID)
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
	b.cost[userID] = costInfo{when: time.Now(), label: label}
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

// ---------- کیبوردها ----------

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
				{Text: btnSessions},
			},
		},
	}
}

func stopKeyboard(sid string) *tgbotapi.InlineKeyboardMarkup {
	data := "stop:" + sid
	return &tgbotapi.InlineKeyboardMarkup{
		InlineKeyboard: [][]tgbotapi.InlineKeyboardButton{{
			{Text: "⏹ توقف این نشست", CallbackData: &data},
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

func (b *Bot) sendProgress(chatID int64, text string, sid string) (int, error) {
	msg := tgbotapi.NewMessage(chatID, text)
	msg.ReplyMarkup = stopKeyboard(sid)
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

// ---------- مدیریت اجراها ----------

func (b *Bot) runFor(sid string) (*runCtl, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	r, ok := b.runs[sid]
	return r, ok
}

func (b *Bot) activeRunsOf(userID int64) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := 0
	for _, r := range b.runs {
		if r.UserID == userID {
			n++
		}
	}
	return n
}

func (b *Bot) addRun(r *runCtl) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.runs[r.SID] = r
}

func (b *Bot) delRun(sid string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if r, ok := b.runs[sid]; ok {
		close(r.done)
		delete(b.runs, sid)
	}
}

func (b *Bot) stopRun(sid string) bool {
	r, ok := b.runFor(sid)
	if !ok {
		return false
	}
	r.cancel()
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	b.oc.Abort(ctx, sid)
	return true
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
	case btnSessions:
		b.openSessions(userID, chatID, 0)
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
	b.submitPrompt(userID, chatID, text, autoLabel(text))
}

func (b *Bot) handleCallback(cq *tgbotapi.CallbackQuery) {
	userID := cq.From.ID
	chatID := cq.Message.Chat.ID
	if !b.allowed(userID) {
		return
	}
	b.api.Request(tgbotapi.NewCallback(cq.ID, ""))
	data := cq.Data
	switch {
	case strings.HasPrefix(data, "s:"):
		b.onSettingsCallback(userID, chatID, cq.Message.MessageID, data)
	case strings.HasPrefix(data, "ss:"):
		b.onSessionsCallback(userID, chatID, cq.Message.MessageID, data)
	case strings.HasPrefix(data, "stop:"):
		sid := strings.TrimPrefix(data, "stop:")
		b.stopRun(sid)
	}
}

// ---------- فایل ----------

func (b *Bot) handleFile(userID, chatID int64, fileID, fileName, caption string, isPhoto bool) {
	path, err := b.download(fileID, fileName)
	if err != nil {
		b.send(chatID, "دریافت فایل ناموفق بود: "+err.Error())
		return
	}
	label := ""
	prompt := strings.TrimSpace(caption)
	if prompt == "" {
		if isPhoto {
			label = "📷 تحلیل تصویر"
			prompt = "این تصویر پیوست‌شده را تحلیل کن و نتیجه را گزارش بده."
		} else {
			label = "📄 بررسی " + fileBase(fileName)
			prompt = "محتوای این فایل پیوست‌شده را بررسی کن و خلاصه یا پاسخ مناسب بده."
		}
	} else {
		label = autoLabel(prompt)
	}
	if label == "" {
		label = autoLabel(prompt)
	}
	b.submitPrompt(userID, chatID, prompt+"\n\nفایل پیوست: "+path, label)
}

func fileBase(name string) string {
	name = filepath.Base(name)
	if len([]rune(name)) > 24 {
		name = string([]rune(name)[:24]) + "…"
	}
	return name
}

// autoLabel نام خودکار نشست از روی موضوع آخرین پیام کاربر
func autoLabel(prompt string) string {
	p := strings.TrimSpace(prompt)
	if i := strings.Index(p, "\n\nفایل پیوست:"); i > 0 {
		p = strings.TrimSpace(p[:i])
	}
	p = strings.Join(strings.Fields(p), " ")
	r := []rune(p)
	if len(r) == 0 {
		return "گفتگو"
	}
	const max = 34
	if len(r) > max {
		return string(r[:max]) + "…"
	}
	return p
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

// ---------- دستورات ----------

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
		b.newSession(userID, chatID)
	case "/sessions":
		b.openSessions(userID, chatID, 0)
	case "/use":
		if arg == "" {
			b.send(chatID, "استفاده: /use <session id>")
			return
		}
		b.setSession(userID, arg)
		st := b.stateFor(userID, chatID)
		b.send(chatID, "نشست فعال شد:\n"+b.sessionLabel(st, arg)+"\n"+arg)
	case "/list":
		b.listSessions(chatID)
	case "/status":
		b.showStatus(userID, chatID)
	case "/agent":
		cur := b.agentFor(userID)
		if arg != "" {
			b.setAgentValue(userID, arg)
			b.send(chatID, "agent فعال: "+arg)
			return
		}
		b.send(chatID, "agent فعلی: "+cur+"\nبا دکمهٔ ⚙️ تنظیمات عوضش کن.")
	case "/cancel":
		st := b.stateOf(userID)
		if st == nil || st.SessionID == "" || !b.stopRun(st.SessionID) {
			b.send(chatID, "اجرایی برای نشست فعال در جریان نیست.")
			return
		}
		b.send(chatID, "اجرای نشست فعال متوقف شد.")
	default:
		b.send(chatID, "دستور ناشناخته. برای راهنما: /help")
	}
}

func (b *Bot) newSession(userID, chatID int64) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := b.oc.CreateSession(ctx)
	if err != nil {
		b.send(chatID, "ساخت نشست ممکن نشد: "+err.Error())
		return
	}
	b.setSession(userID, s.ID)
	st := b.stateFor(userID, chatID)
	b.send(chatID, "نشست جدید ساخته و فعال شد.\n"+b.sessionLabel(st, s.ID)+"\n"+s.ID)
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
	sb.WriteString("آخرین نشست‌های سرور:\n")
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
	st := b.stateOf(userID)
	if st == nil || st.SessionID == "" {
		b.send(chatID, "هنوز نشستی ساخته نشده. /new بزن یا یک متن بفرست.")
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	s, err := b.oc.GetSession(ctx, st.SessionID)
	if err != nil {
		b.send(chatID, "خطا: "+err.Error())
		return
	}
	agent := b.agentFor(userID)
	msg := fmt.Sprintf("نشست فعال: %s\n", s.ID)
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

// ---------- ارسال پرامپت و اجرا ----------

func (b *Bot) submitPrompt(userID, chatID int64, prompt, label string) {
	if !b.ocReady() {
		b.send(chatID, b.ocSetupHint())
		b.openSettings(userID, chatID)
		return
	}
	if b.activeRunsOf(userID) >= maxConcurrentRuns {
		b.send(chatID, fmt.Sprintf("بیشتر از %d اجرای هم‌زمان مجاز نیست؛ صبر کن یکی تمام شود.", maxConcurrentRuns))
		return
	}
	st := b.stateFor(userID, chatID)
	if st.SessionID == "" {
		sid, err := b.ensureSession(userID, chatID)
		if err != nil {
			b.send(chatID, err.Error())
			return
		}
		st = b.stateFor(userID, chatID)
		st.SessionID = sid
	}
	sid := st.SessionID
	if _, busy := b.runFor(sid); busy {
		b.send(chatID, "این نشست الان در حال اجراست.\nبا دکمهٔ 🗂 نشست‌ها یک نشست دیگر بساز یا یکی از نشست‌ها را انتخاب کن تا هم‌زمان جلو برویم.")
		return
	}
	// نام نشست از روی آخرین موضوع
	if label == "" {
		label = autoLabel(prompt)
	}
	b.setSessionLabel(userID, sid, label)
	ctx, cancel := context.WithCancel(context.Background())
	r := &runCtl{SID: sid, UserID: userID, ChatID: chatID, cancel: cancel, done: make(chan struct{}), started: time.Now()}
	b.addRun(r)
	go b.runPrompt(ctx, r, prompt)
}

func (b *Bot) runPrompt(ctx context.Context, r *runCtl, prompt string) {
	defer b.delRun(r.SID)

	agent := b.agentFor(r.UserID)
	progressMsg, err := b.sendProgress(r.ChatID, "در حال انجام…", r.SID)
	if err != nil {
		return
	}

	if err := b.oc.PromptAsync(ctx, r.SID, prompt, agent); err != nil {
		b.edit(r.ChatID, progressMsg, "ارسال دستور ناموفق بود: "+err.Error())
		b.removeInline(r.ChatID, progressMsg)
		return
	}

	final := b.poll(ctx, r, progressMsg)
	b.sendChunks(r.ChatID, final, progressMsg)
}

func (b *Bot) poll(ctx context.Context, r *runCtl, progressMsg int) string {
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
			msg, err := b.oc.LastMessage(ctx, r.SID)
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
					b.oc.Abort(ctx, r.SID)
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
				b.edit(r.ChatID, progressMsg, show)
				lastShown = show
				lastEdit = now
			}
		}
	}
}

// ---------- مدیریت نشست‌ها ----------

func shortSID(sid string) string {
	s := strings.TrimPrefix(sid, "ses_")
	if len(s) > 12 {
		return s[:12]
	}
	return s
}

func (b *Bot) sessionLine(st *UserState, sid string) string {
	_, running := b.runFor(sid)
	line := "`" + shortSID(sid) + "`"
	if sid == st.SessionID {
		line += " ← فعال"
	}
	if running {
		line += " ⏳ در حال اجرا"
	}
	return line
}

func (b *Bot) openSessions(userID, chatID int64, msgID int) {
	st := b.stateFor(userID, chatID)
	b.mu.Lock()
	if st.Labels == nil {
		st.Labels = map[string]string{}
	}
	if st.SessionID != "" {
		had := false
		for _, s := range st.Sessions {
			if s == st.SessionID {
				had = true
				break
			}
		}
		if !had {
			st.Sessions = append([]string{st.SessionID}, st.Sessions...)
		}
	}
	for i, sid := range st.Sessions {
		if st.Labels[sid] == "" {
			st.Labels[sid] = defaultSessionName(i + 1)
		}
	}
	var ids []string
	ids = append(ids, st.Sessions...)
	labels := map[string]string{}
	for k, v := range st.Labels {
		labels[k] = v
	}
	active := st.SessionID
	b.saveStates()
	b.mu.Unlock()

	var sb strings.Builder
	sb.WriteString("🗂 <b>نشست‌ها</b>\n\n")
	if len(ids) == 0 {
		sb.WriteString("هنوز نشستی نداری.\n«➕ نشست جدید» را بزن یا یک متن بفرست تا خودکار ساخته شود.")
	} else {
		for i, sid := range ids {
			_, running := b.runFor(sid)
			num := fmt.Sprintf("%d.", i+1)
			name := labels[sid]
			if name == "" {
				name = defaultSessionName(i + 1)
			}
			line := fmt.Sprintf("%s <b>%s</b>  `%s`", num, name, shortSID(sid))
			if sid == active {
				line += "  ← فعال"
			}
			if running {
				line += "  ⏳"
			}
			sb.WriteString(line + "\n")
		}
		sb.WriteString("\nنام هر نشست خودکار از روی آخرین موضوعش است؛ با ✏️ دستی تغییرش بده و با 🗑 آن را ببند. جابه‌جایی، اجرای در جریان را قطع نمی‌کند.")
	}

	var rows [][]tgbotapi.InlineKeyboardButton
	for i, sid := range ids {
		label := labels[sid]
		if label == "" {
			label = defaultSessionName(i + 1)
		}
		row := []tgbotapi.InlineKeyboardButton{}
		if sid == active {
			row = append(row, inlineBtn("✓ "+clipHead(label, 18), "ss:noop"))
		} else {
			row = append(row, inlineBtn("➡️ "+clipHead(label, 18), "ss:use:"+sid))
		}
		row = append(row, inlineBtn("✏️", "ss:rn:"+sid))
		if _, running := b.runFor(sid); running {
			row = append(row, inlineBtn("⏹", "ss:stop:"+sid))
		}
		row = append(row, inlineBtn("🗑", "ss:del:"+sid))
		rows = append(rows, row)
	}
	rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn("➕ نشست جدید", "ss:new")})
	rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn("🔄 تازه‌سازی", "ss:refresh"), inlineBtn("❌ بستن", "ss:close")})

	kb := rowsOf(rows...)
	if msgID == 0 {
		msg := tgbotapi.NewMessage(chatID, sb.String())
		msg.ParseMode = "HTML"
		msg.ReplyMarkup = kb
		b.api.Send(msg)
		return
	}
	edit := tgbotapi.NewEditMessageText(chatID, msgID, sb.String())
	edit.ParseMode = "HTML"
	edit.ReplyMarkup = kb
	b.api.Send(edit)
}

func (b *Bot) onSessionsCallback(userID, chatID int64, msgID int, data string) {
	switch data {
	case "ss:refresh":
		b.openSessions(userID, chatID, msgID)
	case "ss:close":
		edit := tgbotapi.NewEditMessageText(chatID, msgID, "بسته شد.")
		b.api.Send(edit)
	case "ss:new":
		b.edit(chatID, msgID, "در حال ساخت نشست جدید…")
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		s, err := b.oc.CreateSession(ctx)
		if err != nil {
			b.edit(chatID, msgID, "ساخت نشست ممکن نشد: "+err.Error())
			return
		}
		b.setSession(userID, s.ID)
		st := b.stateFor(userID, chatID)
		b.send(chatID, "✅ نشست جدید ساخته و فعال شد:\n"+b.sessionLabel(st, s.ID)+"\n"+s.ID)
		b.openSessions(userID, chatID, msgID)
	default:
		parts := strings.SplitN(data, ":", 3)
		if len(parts) < 3 {
			return
		}
		switch parts[1] {
		case "use":
			sid := parts[2]
			b.setSession(userID, sid)
			st := b.stateFor(userID, chatID)
			b.send(chatID, "✅ نشست فعال شد:\n"+b.sessionLabel(st, sid)+"\n"+sid)
			b.openSessions(userID, chatID, msgID)
		case "rn":
			sid := parts[2]
			b.setPendingByChat(chatID, "rn:"+sid)
			edit := tgbotapi.NewEditMessageText(chatID, msgID, "✏️ نام جدید این نشست را بفرست (فارسی یا هر اسم دلخواه):")
			b.api.Send(edit)
		case "del":
			sid := parts[2]
			st := b.stateFor(userID, chatID)
			edit := tgbotapi.NewEditMessageText(chatID, msgID, "🗑 نشست «"+b.sessionLabel(st, sid)+"» حذف شود؟\n(اگر اجرایی در جریان باشد متوقف می‌شود.)")
			edit.ReplyMarkup = rowsOf(
				[]tgbotapi.InlineKeyboardButton{inlineBtn("🗑 بله، حذف کن", "ss:delc:"+sid)},
				[]tgbotapi.InlineKeyboardButton{inlineBtn("انصراف", "ss:refresh")},
			)
			b.api.Send(edit)
		case "delc":
			sid := parts[2]
			b.deleteSession(userID, sid)
			b.send(chatID, "🗑 نشست حذف شد.")
			b.openSessions(userID, chatID, msgID)
		case "stop":
			if b.stopRun(parts[2]) {
				b.send(chatID, "اجرای نشست متوقف شد.")
			}
			b.openSessions(userID, chatID, msgID)
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
