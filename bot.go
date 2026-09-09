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
/use <id>    رفتن به نشست مشخص (با فهرست موضوعاتش)
/sessions    باز کردن مدیریت نشست‌ها
/list        فهرست نشست‌های اخیر سرور
/status      جزئیات نشست فعال
/agent       نمایش/تغییر agent
/cancel      توقف اجرای نشست فعال
/help        این راهنما

حین اجرا پاسخ به‌صورت زنده به‌روز می‌شود؛ روی پیام «در حال انجام» دکمهٔ ⏹ توقف همان نشست است.

اگر مدل در میانهٔ کار سؤالی بپرسد (مثل خود CLI)، همان پیام گزینه‌ها را دارد؛ با دکمه‌ها پاسخ بده تا اجرا ادامه یابد. ✏️ یعنی می‌توانی پاسخ خودت را تایپ کنی.`

// botVersion نسخهٔ ربات است؛ هنگام انتشار نسخهٔ جدید آن را به‌روز کن
const botVersion = "v8.2"

const (
	btnStatus    = "📊 وضعیت و هزینه"
	btnSettings  = "⚙️ تنظیمات " + botVersion
	btnSessions  = "🗂 نشست‌ها"
	maxFileBytes = 30 << 20

	// حداکثر اجرای هم‌زمان برای هر کاربر
	maxConcurrentRuns = 6

	// اگر مدل این مدت هیچ متن/فعالیت تازه‌ای تولید نکند، اجرا متوقف می‌شود
	// (وقتی پروایدر پاسخ نمی‌دهد، poll نباید بی‌نهایت «در حال انجام» بماند)
	pollQuietLimit = 6 * time.Minute

	// نمایش «🗂 نشست‌ها» مستقیماً از سرور opencode (نه فقط فهرست محلی ربات)
	maxTrackedSessions = 80  // حداکثر نشستی که ربات در state نگه می‌دارد
	sessionsFetchMax   = 300 // چند نشست اخیر سرور برای همگام‌سازی واکشی شود
	sessionsPageSize   = 10  // هر صفحه از مدیر نشست‌ها چند نشست نشان دهد
)

type UserState struct {
	UserID    int64             `json:"user_id"`
	ChatID    int64             `json:"chat_id"`
	SessionID string            `json:"session_id,omitempty"`
	Sessions  []string          `json:"sessions,omitempty"`
	Labels    map[string]string `json:"labels,omitempty"` // نام نمایشی نشست‌ها
	Manual    map[string]bool   `json:"manual,omitempty"` // نشست‌هایی که کاربر با ✏️ دستی نامشان را عوض کرده
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
	stm    map[string]int64 // زمان ساخت نشست (epoch ms) — برای برچسب خودکار تاریخ‌دار

	qmu  sync.Mutex
	qmap map[string]*pendingQ // توکن کوتاه → سؤال در انتظار پاسخ
	qseq int64

	cat *modelCatalog             // کاتالوگ مدل‌ها (models.dev) با کش
	mlc map[int64]modelsCtx       // صفحهٔ مدل‌های بازشده برای هر چت
	scx map[int64]searchCtx       // نتایج جست‌وجوی باز برای هر چت
	ssp map[int64]int             // آخرین صفحهٔ باز «🗂 نشست‌ها» برای هر چت
	gsm map[int64]bool            // حالت انتخاب گروهی برای هر چت فعال است؟
	gsl map[int64]map[string]bool // sid های انتخاب‌شده در حالت گروهی برای هر چت
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
		stm:    map[string]int64{},
		qmap:   map[string]*pendingQ{},
		cat:    newModelCatalog(filepath.Join(filepath.Dir(cfg.StateFile), "catalog-models.json")),
		mlc:    map[int64]modelsCtx{},
		scx:    map[int64]searchCtx{},
		ssp:    map[int64]int{},
		gsm:    map[int64]bool{},
		gsl:    map[int64]map[string]bool{},
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

// normalizeStates نام پیش‌فرض نشست‌ها را شماره می‌کند (۱، ۲، ۳…)
func (b *Bot) normalizeStates() {
	for _, st := range b.states {
		if st.Labels == nil {
			st.Labels = map[string]string{}
		}
		// مطمئن شو نشست فعال در لیست هست
		if st.SessionID != "" {
			st.Sessions = addSessionID(st.Sessions, st.SessionID)
		}
		renumberAutoLabels(st)
	}
	b.saveStates()
}

// defaultSessionName نام پیش‌فرض نشست: فقط شماره (۱، ۲، ۳…)
func defaultSessionName(n int) string {
	return faNum(n)
}

// faNum اعداد را به رقم فارسی تبدیل می‌کند
func faNum(n int) string {
	r := []rune(strconv.Itoa(n))
	for i, c := range r {
		if c >= '0' && c <= '9' {
			r[i] = '۰' + (c - '0')
		}
	}
	return string(r)
}

// renumberAutoLabels نشست‌های بدون نام دستی را بر اساس جایگاهشان شماره‌گذاری می‌کند
// (نام‌هایی که کاربر با ✏️ گذاشته دست‌نخورده می‌مانند)
func renumberAutoLabels(st *UserState) {
	if st.Labels == nil {
		st.Labels = map[string]string{}
	}
	for i, sid := range st.Sessions {
		if st.Manual != nil && st.Manual[sid] {
			continue
		}
		st.Labels[sid] = defaultSessionName(i + 1)
	}
}

// sessionLabel نام نمایشی نشست؛ نام دستی کاربر همان می‌ماند و نام خودکار
// به «تاریخ و ساعت ساخت» (شمسی، به وقت ایران) تبدیل می‌شود.
func (b *Bot) sessionLabel(st *UserState, sid string) string {
	if st.Manual != nil && st.Manual[sid] {
		if l := st.Labels[sid]; l != "" {
			return l
		}
	}
	if ms, ok := b.createdOf(sid); ok {
		return createdTimeLabel(ms, time.Now())
	}
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

// createdOf زمان ساخت نشست را از کش برمی‌گرداند
func (b *Bot) createdOf(sid string) (int64, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	t, ok := b.stm[sid]
	return t, ok
}

// setCreated زمان ساخت نشست را در کش ثبت می‌کند
func (b *Bot) setCreated(sid string, ms int64) {
	if sid == "" || ms <= 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.stm[sid] = ms
}

// rememberTimes زمان ساخت همهٔ نشست‌های فهرست سرور را در کش ثبت می‌کند
func (b *Bot) rememberTimes(list []OCSession) {
	if len(list) == 0 {
		return
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, s := range list {
		if s.Time.Created > 0 {
			b.stm[s.ID] = s.Time.Created
		}
	}
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

// setSessionLabel نام دستی (با ✏️) نشست را به‌روز می‌کند
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
	if st.Manual == nil {
		st.Manual = map[string]bool{}
	}
	st.Labels[sid] = label
	st.Manual[sid] = true
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
	if st.Manual != nil {
		delete(st.Manual, sid)
	}
	if st.SessionID == sid {
		st.SessionID = ""
	}
	renumberAutoLabels(st)
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
	b.submitPrompt(userID, chatID, text)
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
	case strings.HasPrefix(data, "qa:"):
		b.onQCallback(chatID, data)
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
		b.useSession(userID, chatID, arg)
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
	b.setCreated(s.ID, s.Time.Created)
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

// abortAllRuns همهٔ اجرای‌های فعال را متوقف می‌کند (بعد از تغییر مدل)
// بعد از ری‌استارت سرور، تمام اجرای‌های قبلی بی‌اعتبار می‌شوند
func (b *Bot) abortAllRuns() {
	b.mu.Lock()
	for id, r := range b.runs {
		r.cancel()
		close(r.done)
		delete(b.runs, id)
	}
	b.mu.Unlock()
}

// ---------- ارسال پرامپت و اجرا ----------

func (b *Bot) submitPrompt(userID, chatID int64, prompt string) {
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
		if b.qForChat(chatID) != nil {
			b.send(chatID, "مدل سؤالی پرسیده؛ با دکمه‌های همان پیام پاسخ بده (یا ✏️ بنویس).")
			return
		}
		b.send(chatID, "این نشست الان در حال اجراست.\nبا دکمهٔ 🗂 نشست‌ها یک نشست دیگر بساز یا یکی از نشست‌ها را انتخاب کن تا هم‌زمان جلو برویم.")
		return
	}
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
	lastSig := ""
	lastChange := time.Now()
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
			act := ""
			if msg != nil && msg.Info.Role == "assistant" {
				curText = joinText(msg)
				act = activityLabel(msg)
				if isFinalFinish(msg.Info.Finish) {
					if curText == "" {
						curText = "⚠️ پاسخی دریافت نشد."
					}
					return curText
				}
				if hasPendingQuestion(msg) {
					if b.answerQuestion(ctx, r, progressMsg, curText, msg.Info.ID) {
						return "⛔ متوقف شد."
					}
					// بعد از پاسخ به سؤال، وضعیت زنده را از نو نشان بده
					lastShown = ""
					lastEdit = time.Time{}
					lastSig = ""
					lastChange = now
					continue
				}
			}

			// اگر مدل مدت طولانی هیچ متن/فعالیت تازه‌ای تولید نکرد،
			// احتمالاً پروایدر پاسخ نمی‌دهد؛ حلقه را بی‌نهایت ادامه نده.
			sig := curText + "\x00" + act
			if sig != lastSig {
				lastSig = sig
				lastChange = now
			} else if now.Sub(lastChange) >= pollQuietLimit {
				return "⚠️ بیش از " + pollQuietLimit.String() + " است که مدل هیچ پاسخ یا فعالیتی تولید نکرده؛ احتمالاً پروایدر مشکل دارد.\nبا /new یک نشست تازه بساز یا مدل را در ⚙️ تنظیمات عوض کن."
			}

			interval := 900 * time.Millisecond
			show := preview(curText)
			if curText == "" {
				interval = 2 * time.Second
				label := act
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

// useSession نشست را فعال می‌کند و فهرست موضوعات مطرح‌شده در آن را نشان می‌دهد
func (b *Bot) useSession(userID, chatID int64, sid string) {
	b.setSession(userID, sid)
	st := b.stateFor(userID, chatID)
	name := b.sessionLabel(st, sid)
	full := "✅ نشست فعال شد: " + name + "\n" + sid + "\n\n" + b.sessionTopicsText(sid)
	b.sendChunks(chatID, full, 0)
}

// sessionTopicsText موضوعاتی که تاکنون در نشست مطرح شده‌اند (یک مورد برای هر پیام کاربر)
func (b *Bot) sessionTopicsText(sid string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	msgs, err := b.oc.ListMessages(ctx, sid, 1000)
	if err != nil {
		return "⚠️ فهرست موضوعات در دسترس نبود: " + err.Error()
	}
	var topics []string
	for _, m := range msgs {
		if m.Info.Role != "user" {
			continue
		}
		t := ""
		for _, p := range m.Parts {
			if p.Type == "text" && strings.TrimSpace(p.Text) != "" {
				t = strings.TrimSpace(p.Text)
				break
			}
		}
		if i := strings.Index(t, "\n\nفایل پیوست:"); i > 0 {
			t = t[:i]
		}
		t = collapse(t)
		if t == "" {
			t = "(پیام بدون متن)"
		}
		topics = append(topics, clipHead(t, 140))
	}
	if len(topics) == 0 {
		return "📋 هنوز موضوعی در این نشست مطرح نشده."
	}
	const show = 50
	hidden := 0
	if len(topics) > show {
		hidden = len(topics) - show
		topics = topics[len(topics)-show:]
	}
	var sb strings.Builder
	if hidden > 0 {
		fmt.Fprintf(&sb, "📋 موضوعات این نشست (آخرین %d از %d مورد):\n", show, hidden+show)
	} else {
		fmt.Fprintf(&sb, "📋 موضوعات مطرح‌شده در این نشست (%d مورد):\n", len(topics))
	}
	start := hidden + 1
	for i, t := range topics {
		fmt.Fprintf(&sb, "%s. %s\n", faNum(start+i), t)
	}
	return strings.TrimRight(sb.String(), "\n")
}

func (b *Bot) openSessions(userID, chatID int64, msgID int) {
	b.sessionsPage(userID, chatID, msgID, 0)
}

// lastSSPage آخرین صفحه‌ای که کاربر در مدیر نشست‌ها دیده را برمی‌گرداند
func (b *Bot) lastSSPage(chatID int64) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ssp == nil {
		return 0
	}
	return b.ssp[chatID]
}

func (b *Bot) setSSPage(chatID int64, page int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.ssp == nil {
		b.ssp = map[int64]int{}
	}
	b.ssp[chatID] = page
}

func (b *Bot) setGroupMode(chatID int64, on bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gsm == nil {
		b.gsm = map[int64]bool{}
	}
	b.gsm[chatID] = on
}

func (b *Bot) groupMode(chatID int64) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gsm == nil {
		return false
	}
	return b.gsm[chatID]
}

func (b *Bot) groupSel(chatID int64, sid string) bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gsl == nil {
		return false
	}
	return b.gsl[chatID][sid]
}

func (b *Bot) toggleGroupSel(chatID int64, sid string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gsl == nil {
		b.gsl = map[int64]map[string]bool{}
	}
	if b.gsl[chatID] == nil {
		b.gsl[chatID] = map[string]bool{}
	}
	if b.gsl[chatID][sid] {
		delete(b.gsl[chatID], sid)
	} else {
		b.gsl[chatID][sid] = true
	}
}

func (b *Bot) clearGroupSel(chatID int64) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.gsl != nil {
		delete(b.gsl, chatID)
	}
}

func (b *Bot) groupSelCount(chatID int64) int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.gsl[chatID])
}

func (b *Bot) groupSelList(chatID int64) []string {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []string
	for sid := range b.gsl[chatID] {
		out = append(out, sid)
	}
	return out
}

// sessionsAtSamePage مدیر نشست‌ها را در همان صفحهٔ قبلی دوباره باز می‌کند
func (b *Bot) sessionsAtSamePage(userID, chatID int64, msgID int) {
	b.sessionsPage(userID, chatID, msgID, b.lastSSPage(chatID))
}

// syncLiveSessions فهرست نشست‌های ربات را با sessionهای واقعی سرور همگام می‌کند:
// نشست‌های تازهٔ سرور (مثل نشست‌های ساخته‌شده از CLI) اضافه و نشست‌های حذف‌شده حذف می‌شوند.
// شماره‌گذاری و نام‌های دستی کاربر دست‌نخورده می‌مانند.
func (b *Bot) syncLiveSessions(st *UserState, list []OCSession) {
	onSrv := make(map[string]bool, len(list))
	order := make([]string, 0, len(list))
	for _, s := range list {
		onSrv[s.ID] = true
		order = append(order, s.ID) // سرور به‌ترتیب «آخرین فعالیت» برمی‌گرداند
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if st.Labels == nil {
		st.Labels = map[string]string{}
	}
	if st.Manual == nil {
		st.Manual = map[string]bool{}
	}

	// نشست فعال اگر روی سرور حذف شده باشد دیگر معتبر نیست
	if st.SessionID != "" && !onSrv[st.SessionID] {
		st.SessionID = ""
	}

	// نشست‌هایی که دیگر روی سرور نیستند از فهرست حذف شوند
	keep := st.Sessions[:0]
	for _, sid := range st.Sessions {
		if onSrv[sid] {
			keep = append(keep, sid)
		}
	}
	st.Sessions = keep

	// نشست‌های تازهٔ سرور به انتهای فهرست افزوده شوند (تا شماره‌ها ثابت بمانند)
	have := make(map[string]bool, len(st.Sessions))
	for _, sid := range st.Sessions {
		have[sid] = true
	}
	for _, sid := range order {
		if have[sid] {
			continue
		}
		st.Sessions = append(st.Sessions, sid)
		have[sid] = true
	}

	// برش به حداکثر مجاز؛ نشست فعال/درحال‌اجرا/نام‌گذاری‌شده هرگز حذف نمی‌شود
	if len(st.Sessions) > maxTrackedSessions {
		cut := st.Sessions[:0]
		running := make(map[string]bool, len(b.runs))
		for id := range b.runs {
			running[id] = true
		}
		over := len(st.Sessions) - maxTrackedSessions
		for _, sid := range st.Sessions {
			if over > 0 && sid != st.SessionID && !running[sid] && !st.Manual[sid] {
				over--
				continue
			}
			cut = append(cut, sid)
		}
		st.Sessions = cut
	}

	// پاکسازی برچسب نشست‌هایی که دیگر در فهرست نیستند
	st.Sessions = addSessionID(st.Sessions, st.SessionID)
	kept := make(map[string]bool, len(st.Sessions))
	for _, sid := range st.Sessions {
		kept[sid] = true
	}
	for sid := range st.Labels {
		if !kept[sid] {
			delete(st.Labels, sid)
		}
	}
	for sid := range st.Manual {
		if !kept[sid] {
			delete(st.Manual, sid)
		}
	}

	renumberAutoLabels(st)
	b.saveStates()
}

// sessionsPage یک صفحه از مدیر نشست‌ها را نشان می‌دهد؛ ابتدا از سرور همگام می‌شود
func (b *Bot) sessionsPage(userID, chatID int64, msgID, page int) {
	st := b.stateFor(userID, chatID)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	list, err := b.oc.ListSessions(ctx, sessionsFetchMax)
	cancel()
	var warn error
	if err == nil {
		b.rememberTimes(list)
		b.syncLiveSessions(st, list)
	} else {
		warn = err
	}

	b.mu.Lock()
	ids := make([]string, len(st.Sessions))
	copy(ids, st.Sessions)
	active := st.SessionID
	b.mu.Unlock()

	if len(ids) == 0 {
		text := "🗂 <b>نشست‌ها</b>\n\n"
		if warn != nil {
			text += "⚠️ سرور در دسترس نبود.\n"
		}
		text += "هنوز نشستی روی سرور نیست.\n«➕ نشست جدید» را بزن یا یک متن بفرست تا خودکار ساخته شود."
		kb := rowsOf([]tgbotapi.InlineKeyboardButton{inlineBtn("➕ نشست جدید", "ss:new")})
		if msgID == 0 {
			msg := tgbotapi.NewMessage(chatID, text)
			msg.ParseMode = "HTML"
			msg.ReplyMarkup = kb
			b.api.Send(msg)
		} else {
			edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
			edit.ParseMode = "HTML"
			edit.ReplyMarkup = kb
			b.api.Send(edit)
		}
		return
	}

	total := len(ids)
	pages := (total + sessionsPageSize - 1) / sessionsPageSize
	if page < 0 {
		page = 0
	}
	if page >= pages {
		page = pages - 1
	}
	start := page * sessionsPageSize
	end := start + sessionsPageSize
	if end > total {
		end = total
	}
	b.setSSPage(chatID, page)

	var sb strings.Builder
	sb.WriteString("🗂 <b>نشست‌ها</b>")
	if pages > 1 {
		fmt.Fprintf(&sb, "  (صفحه %s از %s)", faNum(page+1), faNum(pages))
	}
	group := b.groupMode(chatID)
	if group {
		if n := b.groupSelCount(chatID); n > 0 {
			fmt.Fprintf(&sb, "  — 🔀 %s نشست انتخاب شده", faNum(n))
		} else {
			sb.WriteString("  — 🔀 حالت انتخاب گروهی")
		}
	}
	sb.WriteString("\n")
	if warn != nil {
		sb.WriteString("⚠️ سرور در دسترس نبود؛ نشست‌های ذخیره‌شده نمایش داده می‌شود.\n")
	}
	for i := start; i < end; i++ {
		sid := ids[i]
		_, running := b.runFor(sid)
		name := b.sessionLabel(st, sid)
		line := fmt.Sprintf("<b>%s</b>  `%s`", name, shortSID(sid))
		if group && b.groupSel(chatID, sid) {
			line = "☑️ " + line
		}
		if sid == active {
			line += "  ← فعال"
		}
		if running {
			line += "  ⏳"
		}
		sb.WriteString(line + "\n")
	}
	if group {
		sb.WriteString("\nروی هر نشست بزن تا انتخاب/لغو شود، بعد «🗑 حذف انتخاب‌ها» را بزن.")
	} else {
		sb.WriteString("\nفهرست همان نشست‌های واقعی سرور است؛ 🗑 نشست را روی سرور هم برای همیشه حذف می‌کند.")
	}

	var rows [][]tgbotapi.InlineKeyboardButton
	for i := start; i < end; i++ {
		sid := ids[i]
		label := b.sessionLabel(st, sid)
		if group {
			mark := "☑️"
			if b.groupSel(chatID, sid) {
				mark = "✅"
			}
			rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn(mark+" "+clipHead(label, 24), "ss:gtgl:"+sid)})
			continue
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
	if pages > 1 {
		nav := []tgbotapi.InlineKeyboardButton{}
		if page > 0 {
			nav = append(nav, inlineBtn("◀️ قبلی", "ss:pg:"+strconv.Itoa(page-1)))
		} else {
			nav = append(nav, inlineBtn("⏺", "ss:noop"))
		}
		nav = append(nav, inlineBtn(fmt.Sprintf("%s/%s", faNum(page+1), faNum(pages)), "ss:noop"))
		if page+1 < pages {
			nav = append(nav, inlineBtn("بعدی ▶️", "ss:pg:"+strconv.Itoa(page+1)))
		} else {
			nav = append(nav, inlineBtn("⏺", "ss:noop"))
		}
		rows = append(rows, nav)
	}
	if group {
		del := "🗑 حذف انتخاب‌ها"
		if n := b.groupSelCount(chatID); n > 0 {
			del = "🗑 حذف انتخاب‌ها (" + faNum(n) + ")"
		}
		rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn(del, "ss:gdel"), inlineBtn("✖️ لغو حالت گروهی", "ss:gcncl")})
	}
	rows = append(rows,
		[]tgbotapi.InlineKeyboardButton{inlineBtn("➕ نشست جدید", "ss:new"), inlineBtn("🔄 تازه‌سازی", "ss:refresh"), inlineBtn("❌ بستن", "ss:close")},
	)
	if !group {
		rows = append(rows,
			[]tgbotapi.InlineKeyboardButton{inlineBtn("🔀 عملیات گروهی", "ss:gm")},
		)
	}

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
		b.sessionsAtSamePage(userID, chatID, msgID)
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
		b.sessionsAtSamePage(userID, chatID, msgID)
	case "ss:gm":
		b.setGroupMode(chatID, true)
		b.clearGroupSel(chatID)
		b.sessionsAtSamePage(userID, chatID, msgID)
	case "ss:gcncl":
		b.setGroupMode(chatID, false)
		b.clearGroupSel(chatID)
		b.sessionsAtSamePage(userID, chatID, msgID)
	case "ss:gdel":
		b.openGroupDeleteConfirm(userID, chatID, msgID)
	default:
		parts := strings.SplitN(data, ":", 3)
		if len(parts) < 3 {
			return
		}
		switch parts[1] {
		case "pg":
			if p, err := strconv.Atoi(parts[2]); err == nil {
				b.sessionsPage(userID, chatID, msgID, p)
			}
		case "use":
			sid := parts[2]
			b.useSession(userID, chatID, sid)
			b.sessionsAtSamePage(userID, chatID, msgID)
		case "rn":
			sid := parts[2]
			b.setPendingByChat(chatID, "rn:"+sid)
			edit := tgbotapi.NewEditMessageText(chatID, msgID, "✏️ نام جدید این نشست را بفرست (فارسی یا هر اسم دلخواه):")
			b.api.Send(edit)
		case "del":
			sid := parts[2]
			st := b.stateFor(userID, chatID)
			edit := tgbotapi.NewEditMessageText(chatID, msgID, "🗑 نشست «"+b.sessionLabel(st, sid)+"» برای همیشه حذف شود؟\nاین نشست و تمام گفتگویش از روی سرور opencode پاک می‌شود (غیرقابل بازگشت). اجرای در جریان هم متوقف می‌شود.")
			edit.ReplyMarkup = rowsOf(
				[]tgbotapi.InlineKeyboardButton{inlineBtn("🗑 بله، حذف کن", "ss:delc:"+sid)},
				[]tgbotapi.InlineKeyboardButton{inlineBtn("انصراف", "ss:refresh")},
			)
			b.api.Send(edit)
		case "delc":
			sid := parts[2]
			b.stopRun(sid)
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			err := b.oc.DeleteSession(ctx, sid)
			cancel()
			if err != nil {
				b.send(chatID, "❌ حذف نشست روی سرور ممکن نشد: "+err.Error())
				b.sessionsAtSamePage(userID, chatID, msgID)
				return
			}
			b.deleteSession(userID, sid)
			b.send(chatID, "🗑 نشست از روی سرور حذف شد.")
			b.sessionsAtSamePage(userID, chatID, msgID)
		case "gtgl":
			b.toggleGroupSel(chatID, parts[2])
			b.sessionsAtSamePage(userID, chatID, msgID)
		case "gdelc":
			b.deleteGroupSessions(userID, chatID, msgID)
		case "stop":
			if b.stopRun(parts[2]) {
				b.send(chatID, "اجرای نشست متوقف شد.")
			}
			b.sessionsAtSamePage(userID, chatID, msgID)
		}
	}
}

// openGroupDeleteConfirm تأیید حذف دسته‌جمعی نشست‌های انتخاب‌شده را نشان می‌دهد
func (b *Bot) openGroupDeleteConfirm(userID, chatID int64, msgID int) {
	sids := b.groupSelList(chatID)
	if len(sids) == 0 {
		edit := tgbotapi.NewEditMessageText(chatID, msgID, "هیچ نشستی انتخاب نشده. روی نشست‌ها بزن تا انتخاب شوند.")
		edit.ReplyMarkup = rowsOf([]tgbotapi.InlineKeyboardButton{inlineBtn("🔄 بازگشت", "ss:refresh")})
		b.api.Send(edit)
		return
	}
	st := b.stateFor(userID, chatID)
	var names []string
	for i, sid := range sids {
		if i >= 8 {
			names = append(names, "…")
			break
		}
		names = append(names, "• "+b.sessionLabel(st, sid))
	}
	text := fmt.Sprintf("🗑 <b>%s نشست</b> برای همیشه حذف شوند؟\nاین نشست‌ها و تمام گفتگویشان از روی سرور opencode پاک می‌شود (غیرقابل بازگشت). اجرای در جریان هم متوقف می‌شود.\n\n%s", faNum(len(sids)), strings.Join(names, "\n"))
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = "HTML"
	edit.ReplyMarkup = rowsOf(
		[]tgbotapi.InlineKeyboardButton{inlineBtn("🗑 بله، همه را حذف کن", "ss:gdelc")},
		[]tgbotapi.InlineKeyboardButton{inlineBtn("انصراف", "ss:refresh")},
	)
	b.api.Send(edit)
}

// deleteGroupSessions نشست‌های انتخاب‌شده را یک‌به‌یک روی سرور حذف می‌کند
func (b *Bot) deleteGroupSessions(userID, chatID int64, msgID int) {
	sids := b.groupSelList(chatID)
	if len(sids) == 0 {
		b.sessionsAtSamePage(userID, chatID, msgID)
		return
	}
	b.clearGroupSel(chatID)
	b.setGroupMode(chatID, false)
	b.edit(chatID, msgID, "در حال حذف "+faNum(len(sids))+" نشست…")

	ok, fail := 0, 0
	var errMsg []string
	for _, sid := range sids {
		b.stopRun(sid)
		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		err := b.oc.DeleteSession(ctx, sid)
		cancel()
		if err != nil {
			fail++
			if len(errMsg) < 3 {
				errMsg = append(errMsg, "• "+shortSID(sid)+": "+err.Error())
			}
			continue
		}
		b.deleteSession(userID, sid)
		ok++
	}
	text := fmt.Sprintf("🗑 حذف گروهی انجام شد: %s نشست حذف شد.", faNum(ok))
	if fail > 0 {
		text += fmt.Sprintf("\n⚠️ %s نشست حذف نشد.", faNum(fail))
		if len(errMsg) > 0 {
			text += "\n" + strings.Join(errMsg, "\n")
		}
	}
	b.send(chatID, text)
	b.sessionsPage(userID, chatID, msgID, 0)
}

func isFinalFinish(finish string) bool {
	switch finish {
	case "", "pending", "tool-calls":
		return false
	}
	return true
}

func hasPendingQuestion(msg *OCMessage) bool {
	for _, p := range msg.Parts {
		if p.Type != "tool" || p.Tool != "question" || p.State == nil {
			continue
		}
		if p.State.Status == "completed" || p.State.Status == "error" {
			continue
		}
		return true
	}
	return false
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
