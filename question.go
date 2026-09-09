package main

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"

	"opencode-tg-bot/internal/occlient"
)

// ---------- سؤال تعاملی مثل CLI خود opencode ----------

type qEvent struct {
	kind int
	opt  int
	text string
}

const (
	qEvTap    = iota // زدن روی یک گزینه
	qEvOK            // تأیید (چندگزینهای یا بدون انتخاب)
	qEvText          // پاسخ آزاد تایپشده
	qEvCustom        // درخواست «نوشتن پاسخ خودم»
	qEvCancel        // رد سؤال و ادامه
)

// pendingQ یک سؤالِ در انتظارِ پاسخ؛ صاحب آن گوروتین poll است و همهٔ
// ویرایشهای پیام را همان گوروتین انجام میدهد تا نژاد (race) پیش نیاید.
type pendingQ struct {
	token  string
	sid    string
	reqID  string
	chatID int64
	msgID  int
	info   []occlient.QuestionInfo
	sel    [][]string // پاسخ هر سؤال = فهرست برچسبهای انتخابشده
	idx    int        // سؤالِ در حال پاسخ (از 0)
	mode   string     // "choice" | "text"
	ev     chan qEvent
}

// qReg ثبت سؤال‌های تعاملی در انتظار پاسخ؛ توکن کوتاه → pendingQ.
type qReg struct {
	mu   sync.Mutex
	qmap map[string]*pendingQ
	seq  int64
}

func newQReg() *qReg {
	return &qReg{qmap: map[string]*pendingQ{}}
}

func (q *qReg) newToken() string {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.seq++
	return fmt.Sprintf("q%x%x", uint32(time.Now().UnixNano()), q.seq&0xffff)
}

func (q *qReg) put(p *pendingQ) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.qmap[p.token] = p
}

func (q *qReg) byToken(token string) *pendingQ {
	q.mu.Lock()
	defer q.mu.Unlock()
	return q.qmap[token]
}

func (q *qReg) forChat(chatID int64) *pendingQ {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, p := range q.qmap {
		if p.chatID == chatID {
			return p
		}
	}
	return nil
}

func (q *qReg) del(token string) {
	q.mu.Lock()
	defer q.mu.Unlock()
	delete(q.qmap, token)
}

func (b *Bot) qCleanup(p *pendingQ) {
	b.qs.del(p.token)
	// اگر در حالت «پاسخ آزاد» بودیم، pending ثبت‌شده را پاک کن
	b.users.clearPendingToken(p.token)
}

func (b *Bot) qSendEvent(p *pendingQ, ev qEvent) {
	select {
	case p.ev <- ev:
	default:
	}
}

func customAllowed(q occlient.QuestionInfo) bool {
	return q.Custom == nil || *q.Custom
}

func containsSel(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

func toggleSel(list []string, s string) []string {
	var out []string
	had := false
	for _, v := range list {
		if v == s {
			had = true
			continue
		}
		out = append(out, v)
	}
	if !had {
		out = append(out, s)
	}
	return out
}

// answerQuestion سؤالِ پیدا شده را با دکمههای تلگرام نشان میدهد، منتظر
// پاسخ کاربر میماند و پاسخ را به opencode میفرستد تا اجرا ادامه یابد.
// خروجی true یعنی کاربر اجرا را متوقف کرده است.
func (b *Bot) answerQuestion(ctx context.Context, r *runCtl, progressMsg int, partial, msgID string) bool {
	req, ok := b.waitQuestionReq(ctx, r.SID, msgID)
	if !ok {
		return ctx.Err() != nil
	}
	if len(req.Questions) == 0 {
		return false
	}
	p := &pendingQ{
		token:  b.qs.newToken(),
		sid:    r.SID,
		reqID:  req.ID,
		chatID: r.ChatID,
		msgID:  progressMsg,
		info:   req.Questions,
		sel:    make([][]string, len(req.Questions)),
		mode:   "choice",
		ev:     make(chan qEvent, 16),
	}
	b.qs.put(p)
	defer b.qCleanup(p)

	b.renderQ(p, partial)
	for {
		select {
		case <-ctx.Done():
			return true
		case ev := <-p.ev:
			if b.qStep(ctx, p, ev) {
				return false
			}
		}
	}
}

// waitQuestionReq درخواست سؤالِ متعلق به همین پیام/نشست را پیدا میکند.
// ابزارِ سؤال ممکن است کمی دیرتر در فهرست /question ثبت شود، پس چند بار تلاش میکنیم.
func (b *Bot) waitQuestionReq(ctx context.Context, sid, msgID string) (*occlient.QuestionRequest, bool) {
	for i := 0; i < 8; i++ {
		if ctx.Err() != nil {
			return nil, false
		}
		if qs, err := b.oc.ListQuestions(ctx); err == nil {
			for j := range qs {
				if qs[j].Tool != nil && qs[j].Tool.MessageID == msgID && qs[j].SessionID == sid {
					return &qs[j], true
				}
			}
			var sidMatch []*occlient.QuestionRequest
			for j := range qs {
				if qs[j].SessionID == sid {
					sidMatch = append(sidMatch, &qs[j])
				}
			}
			if len(sidMatch) == 1 {
				return sidMatch[0], true
			}
		}
		select {
		case <-ctx.Done():
			return nil, false
		case <-time.After(250 * time.Millisecond):
		}
	}
	return nil, false
}

// renderQ سؤالِ فعلی را روی پیامِ پیشرفت رسم میکند. فقط گوروتین صاحب p صدا بزند.
func (b *Bot) renderQ(p *pendingQ, partial string) {
	if p.idx < 0 || p.idx >= len(p.info) {
		return
	}
	q := p.info[p.idx]
	var sb strings.Builder
	sb.WriteString("🤔 ")
	if h := clipHead(q.Header, 40); h != "" {
		sb.WriteString("[" + h + "] ")
	}
	sb.WriteString(q.Question)
	fmt.Fprintf(&sb, "\n\n📌 سؤال %d از %d", p.idx+1, len(p.info))

	if p.mode == "text" {
		sb.WriteString("\n\nپاسخ خودت را مستقیم تایپ و بفرست.\n(برای توقف: /cancel)")
		b.editKeyboard(p.chatID, p.msgID, sb.String(), &tgbotapi.InlineKeyboardMarkup{})
		return
	}

	switch {
	case q.Multiple:
		sb.WriteString("\n(چندگزینهای؛ گزینهها را بزن و بعد «✔️ تأیید» کن)")
	case len(q.Options) > 1:
		sb.WriteString("\n(یکی از گزینهها را انتخاب کن)")
	}
	for _, o := range q.Options {
		sb.WriteString("\n\n")
		mark := "• "
		if q.Multiple && containsSel(p.sel[p.idx], o.Label) {
			mark = "✅ "
		}
		sb.WriteString(mark + clipHead(o.Label, 80))
		if d := collapse(o.Description); d != "" {
			sb.WriteString("\n    " + clipHead(d, 160))
		}
	}

	var rows [][]tgbotapi.InlineKeyboardButton
	for i, o := range q.Options {
		label := clipHead(o.Label, 60)
		if q.Multiple && containsSel(p.sel[p.idx], o.Label) {
			label = "☑️ " + label
		}
		rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn(label, "qa:"+p.token+":o"+strconv.Itoa(i))})
	}
	if q.Multiple {
		rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn("✔️ تأیید و بعدی", "qa:"+p.token+":ok")})
	} else if len(q.Options) == 0 {
		rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn("بعدی (بدون انتخاب)", "qa:"+p.token+":ok")})
	}
	if customAllowed(q) {
		rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn("✏️ نوشتن پاسخ خودم", "qa:"+p.token+":tx")})
	}
	rows = append(rows,
		[]tgbotapi.InlineKeyboardButton{inlineBtn("❌ رد سؤال و ادامه", "qa:"+p.token+":x")},
		[]tgbotapi.InlineKeyboardButton{inlineBtn("⏹ توقف نشست", "stop:"+p.sid)},
	)
	b.editKeyboard(p.chatID, p.msgID, sb.String(), rowsOf(rows...))
}

// qStep یک رویداد کاربر را اعمال میکند؛ خروجی true یعنی کار این سؤال تمام شده.
func (b *Bot) qStep(ctx context.Context, p *pendingQ, ev qEvent) bool {
	if p.idx >= len(p.info) {
		return b.qFinish(ctx, p, false)
	}
	q := p.info[p.idx]
	switch ev.kind {
	case qEvTap:
		if ev.opt < 0 || ev.opt >= len(q.Options) {
			return false
		}
		if q.Multiple {
			p.sel[p.idx] = toggleSel(p.sel[p.idx], q.Options[ev.opt].Label)
			b.renderQ(p, "")
			return false
		}
		p.sel[p.idx] = []string{q.Options[ev.opt].Label}
		p.idx++
	case qEvOK:
		p.idx++
	case qEvText:
		if t := strings.TrimSpace(ev.text); t != "" {
			p.sel[p.idx] = []string{t}
		}
		p.idx++
	case qEvCustom:
		p.mode = "text"
		b.setPendingByChat(p.chatID, "qtext:"+p.token)
		b.renderQ(p, "")
		return false
	case qEvCancel:
		return b.qFinish(ctx, p, true)
	}
	if p.idx >= len(p.info) {
		return b.qFinish(ctx, p, false)
	}
	p.mode = "choice"
	b.renderQ(p, "")
	return false
}

// qFinish پاسخها را به opencode میفرستد (یا سؤال را رد میکند) تا مدل ادامه دهد.
func (b *Bot) qFinish(ctx context.Context, p *pendingQ, reject bool) bool {
	b.editKeyboard(p.chatID, p.msgID, "در حال ادامه…", stopKeyboard(p.sid))
	nctx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	if reject {
		_ = b.oc.RejectQuestion(nctx, p.reqID)
	} else {
		_ = b.oc.ReplyQuestion(nctx, p.reqID, p.sel)
	}
	return true
}

func (b *Bot) editKeyboard(chatID int64, msgID int, text string, kb *tgbotapi.InlineKeyboardMarkup) {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	if kb != nil {
		edit.ReplyMarkup = kb
	}
	b.api.Send(edit)
}

// ---------- کلیک روی دکمههای سؤال ----------

func (b *Bot) onQCallback(chatID int64, data string) {
	parts := strings.Split(data, ":")
	if len(parts) != 3 {
		return
	}
	token, action := parts[1], parts[2]
	p := b.qs.byToken(token)
	if p == nil || p.chatID != chatID {
		return
	}
	switch {
	case action == "ok":
		b.qSendEvent(p, qEvent{kind: qEvOK})
	case action == "tx":
		b.setPendingByChat(chatID, "qtext:"+p.token)
		b.qSendEvent(p, qEvent{kind: qEvCustom})
	case action == "x":
		b.qSendEvent(p, qEvent{kind: qEvCancel})
	case strings.HasPrefix(action, "o"):
		n, err := strconv.Atoi(strings.TrimPrefix(action, "o"))
		if err == nil {
			b.qSendEvent(p, qEvent{kind: qEvTap, opt: n})
		}
	}
}
