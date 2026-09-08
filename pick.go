package main

import (
	"fmt"
	"strconv"
	"strings"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ---------- انتخاب پروایدر/مدل: فهرست کامل، صفحه‌بندی‌شده و قابل جست‌وجو ----------

type provItem struct {
	id    string
	label string
}

// modelsCtx زمینهٔ صفحهٔ مدل‌های یک پروایدر برای هر چت
type modelsCtx struct {
	pid    string
	filter string
}

// searchCtx زمینهٔ نتایج جست‌وجو برای هر چت
type searchCtx struct {
	query string
	mode  string
}

const (
	provPerPage  = 8
	modelPerPage = 8
)

func (b *Bot) setMlc(chatID int64, c modelsCtx) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.mlc[chatID] = c
}

func (b *Bot) getMlc(chatID int64) (modelsCtx, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.mlc[chatID]
	return c, ok
}

func (b *Bot) setScx(chatID int64, c searchCtx) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.scx[chatID] = c
}

func (b *Bot) getScx(chatID int64) (searchCtx, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	c, ok := b.scx[chatID]
	return c, ok
}

func (b *Bot) sendOrEdit(chatID int64, msgID int, text string, kb *tgbotapi.InlineKeyboardMarkup) {
	if msgID == 0 {
		msg := tgbotapi.NewMessage(chatID, text)
		if kb != nil {
			msg.ReplyMarkup = kb
		}
		b.api.Send(msg)
		return
	}
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	if kb != nil {
		edit.ReplyMarkup = kb
	}
	b.api.Send(edit)
}

// ---------- فهرست پروایدرها ----------

func (b *Bot) provItems() []provItem {
	env := b.envFor()
	keyed := map[string]bool{}
	for _, id := range env.providersConfigured() {
		keyed[id] = true
	}
	var items []provItem
	seen := map[string]bool{}
	add := func(id, label string) {
		if id == "" || seen[id] {
			return
		}
		seen[id] = true
		if keyed[id] {
			label += " ✅"
		}
		items = append(items, provItem{id: id, label: label})
	}
	for _, p := range presetProviders {
		add(p.id, p.label)
	}
	for _, id := range env.providersConfigured() {
		add(id, id)
	}
	if provs, err := b.cat.providers(); err == nil {
		for _, p := range provs {
			add(p.ID, p.Name)
		}
	}
	return items
}

// searchProvHits پروایدرهای مچ‌شده با query را برمی‌گرداند.
// برگشت دوم یعنی کاتالوگ در دسترس نبود و فقط روی فهرست محدود جست‌وجو شد.
func (b *Bot) searchProvHits(q string) ([]provItem, bool) {
	env := b.envFor()
	keyed := map[string]bool{}
	for _, id := range env.providersConfigured() {
		keyed[id] = true
	}
	presetLabel := map[string]string{}
	for _, p := range presetProviders {
		presetLabel[p.id] = p.label
	}
	ql := strings.ToLower(strings.TrimSpace(q))

	provs, err := b.cat.searchProvs(ql)
	if err == nil {
		var hits []provItem
		for _, p := range provs {
			label := p.Name
			if pl, ok := presetLabel[p.ID]; ok {
				label = pl
			}
			if keyed[p.ID] {
				label += " ✅"
			}
			hits = append(hits, provItem{id: p.ID, label: label})
		}
		return hits, false
	}

	// fallback بدون اینترنت: فقط فهرست داخلی/سفارشی
	var hits []provItem
	for _, it := range b.provItems() {
		if strings.Contains(strings.ToLower(it.id), ql) || strings.Contains(strings.ToLower(it.label), ql) {
			hits = append(hits, it)
		}
	}
	return hits, true
}

func (b *Bot) provTitle(mode string) string {
	switch mode {
	case "key":
		return "🔑 کلید API کدام پروایدر؟\nبعد از انتخاب، کلید را تایپ و ارسال کن."
	default:
		return "🧠 مدل کدام پروایدر؟"
	}
}

// renderProvScreen یک فهرست صفحه‌بندی‌شده از پروایدرها می‌سازد.
// search=true یعنی این فهرست نتیجهٔ جست‌وجوی کاربر است (سؤال‌ها در scx هستند).
func (b *Bot) renderProvScreen(chatID int64, msgID int, mode string, items []provItem, page int, search bool) {
	total := len(items)
	if total == 0 {
		b.sendOrEdit(chatID, msgID, "چیزی پیدا نشد. 🔍 یا ✍️ دستی را امتحان کن.", nil)
		return
	}
	last := (total + provPerPage - 1) / provPerPage
	if page < 0 {
		page = 0
	}
	if page >= last {
		page = last - 1
	}
	var sb strings.Builder
	sb.WriteString(b.provTitle(mode))
	if search {
		sb.WriteString("\n🔎 نتیجهٔ جست‌وجو:")
	}
	sb.WriteString(fmt.Sprintf("\n\n(صفحه %d از %d — %d پروایدر)", page+1, last, total))
	if !search && mode != "key" {
		sb.WriteString("\n✅ = کلیدش ست شده")
	}

	start := page * provPerPage
	end := start + provPerPage
	if end > total {
		end = total
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	var row []tgbotapi.InlineKeyboardButton
	for _, it := range items[start:end] {
		label := clipHead(it.label, 24)
		var data string
		switch {
		case mode == "key":
			data = "s:kp:" + it.id
		case search:
			data = "s:ps:" + it.id
		default:
			data = "s:pv:" + it.id
		}
		row = append(row, inlineBtn(label, data))
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}

	var nav []tgbotapi.InlineKeyboardButton
	if page > 0 {
		if search {
			nav = append(nav, inlineBtn("◀️ قبلی", "s:sp:"+strconv.Itoa(page-1)))
		} else {
			nav = append(nav, inlineBtn("◀️ قبلی", "s:pl:"+mode+":"+strconv.Itoa(page-1)))
		}
	}
	if page+1 < last {
		if search {
			nav = append(nav, inlineBtn("بعدی ▶️", "s:sp:"+strconv.Itoa(page+1)))
		} else {
			nav = append(nav, inlineBtn("بعدی ▶️", "s:pl:"+mode+":"+strconv.Itoa(page+1)))
		}
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}

	var tools []tgbotapi.InlineKeyboardButton
	tools = append(tools, inlineBtn("🔍 جست‌وجو", "s:q:"+mode))
	if mode != "key" {
		tools = append(tools, inlineBtn("✍️ تایپ دستی", "s:mm:__manual__"))
	}
	rows = append(rows, tools)
	rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn("🔙 بستن", "s:main")})

	b.sendOrEdit(chatID, msgID, sb.String(), rowsOf(rows...))
}

// ---------- مدل‌های یک پروایدر ----------

// modelListFor مدل‌های پروایدر را (با فیلتر اختیاری) برمی‌گرداند؛ اگر کاتالوگ
// در دسترس نبود از فهرست داخلی presetها استفاده می‌کند.
func (b *Bot) modelListFor(pid, filter string) ([]catModel, bool) {
	if mods, known, err := b.cat.modelsOf(pid, filter); err == nil {
		if known {
			return mods, true
		}
	}
	// fallback: پروایدرهای داخلی
	if p, ok := presetByID(pid); ok {
		var mods []catModel
		for _, id := range p.models {
			mods = append(mods, catModel{ID: id, Name: id})
		}
		if f := strings.ToLower(strings.TrimSpace(filter)); f != "" {
			var out []catModel
			for _, m := range mods {
				if strings.Contains(strings.ToLower(m.ID), f) {
					out = append(out, m)
				}
			}
			mods = out
		}
		return mods, true
	}
	return nil, false
}

func (b *Bot) showModelsFor(chatID int64, msgID int, pid, filter string) {
	b.setMlc(chatID, modelsCtx{pid: pid, filter: filter})
	b.renderModelsScreen(chatID, msgID, 0)
}

func (b *Bot) renderModelsScreen(chatID int64, msgID int, page int) {
	ctx, ok := b.getMlc(chatID)
	if !ok {
		return
	}
	pid := ctx.pid
	filter := ctx.filter

	env := b.envFor()
	label := pid
	if p, ok := presetByID(pid); ok {
		label = p.label
	} else if pr, ok := b.cat.provider(pid); ok {
		label = pr.Name
	}

	mods, known := b.modelListFor(pid, filter)
	if !known {
		b.sendOrEdit(chatID, msgID,
			"پروایدر «"+pid+"» نه در کاتالوگ است و نه در لیست داخلی.\nبا ✍️ تایپ دستی مدل را به‌صورت provider/model بفرست.",
			rowsOf([]tgbotapi.InlineKeyboardButton{inlineBtn("✍️ تایپ دستی", "s:mm:"+pid)},
				[]tgbotapi.InlineKeyboardButton{inlineBtn("🔙 فهرست پروایدرها", "s:model")}))
		return
	}

	total := len(mods)
	if total == 0 {
		b.sendOrEdit(chatID, msgID,
			fmt.Sprintf("مدلی با این فیلتر در «%s» پیدا نشد.", clipHead(label, 40)),
			rowsOf(
				[]tgbotapi.InlineKeyboardButton{inlineBtn("✕ حذف فیلتر", "s:pv:"+pid)},
				[]tgbotapi.InlineKeyboardButton{inlineBtn("✍️ تایپ دستی", "s:mm:"+pid)},
				[]tgbotapi.InlineKeyboardButton{inlineBtn("🔙 فهرست پروایدرها", "s:model")}))
		return
	}

	last := (total + modelPerPage - 1) / modelPerPage
	if page < 0 {
		page = 0
	}
	if page >= last {
		page = last - 1
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "🧠 مدل‌های «%s»:", clipHead(label, 40))
	if filter != "" {
		fmt.Fprintf(&sb, "\n🔎 فیلتر: «%s»", clipHead(filter, 40))
	}
	fmt.Fprintf(&sb, "\n(صفحه %d از %d — %d مدل)", page+1, last, total)

	start := page * modelPerPage
	end := start + modelPerPage
	if end > total {
		end = total
	}
	var rows [][]tgbotapi.InlineKeyboardButton
	cur := env.currentModel()
	for i, m := range mods[start:end] {
		label2 := clipHead(m.ID, 50)
		if cur == pid+"/"+m.ID {
			label2 += " ← فعلی"
		}
		rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn(label2, "s:sel:"+pid+":"+strconv.Itoa(start+i))})
	}

	var nav []tgbotapi.InlineKeyboardButton
	if page > 0 {
		nav = append(nav, inlineBtn("◀️ قبلی", "s:ml:"+strconv.Itoa(page-1)))
	}
	if page+1 < last {
		nav = append(nav, inlineBtn("بعدی ▶️", "s:ml:"+strconv.Itoa(page+1)))
	}
	if len(nav) > 0 {
		rows = append(rows, nav)
	}

	var tools []tgbotapi.InlineKeyboardButton
	tools = append(tools, inlineBtn("🔍 فیلتر مدل‌ها", "s:mf:"+pid))
	if filter != "" {
		tools = append(tools, inlineBtn("✕ حذف فیلتر", "s:pv:"+pid))
	}
	rows = append(rows, tools)
	rows = append(rows,
		[]tgbotapi.InlineKeyboardButton{inlineBtn("✍️ تایپ دستی", "s:mm:"+pid)},
		[]tgbotapi.InlineKeyboardButton{inlineBtn("🔙 فهرست پروایدرها", "s:model")},
	)

	b.sendOrEdit(chatID, msgID, sb.String(), rowsOf(rows...))
}

func (b *Bot) pickModel(chatID int64, msgID int, pid, idxStr string) {
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		return
	}
	ctx, ok := b.getMlc(chatID)
	if !ok {
		return
	}
	if ctx.pid != pid {
		// زمینه قدیمی است؛ با فهرست کامل پروایدر دوباره باز کن
		b.setMlc(chatID, modelsCtx{pid: pid, filter: ""})
		ctx, _ = b.getMlc(chatID)
	}
	mods, known := b.modelListFor(ctx.pid, ctx.filter)
	if !known || idx < 0 || idx >= len(mods) {
		return
	}
	b.applyModel(chatID, msgID, pid+"/"+mods[idx].ID)
}

func parsePage(s string) int {
	n, err := strconv.Atoi(s)
	if err != nil {
		return 0
	}
	return n
}

// doSearch جست‌وجوی پروایدر/مدل را اجرا و نتیجه را (به‌صورت صفحه‌بندی‌شده) نشان می‌دهد.
func (b *Bot) doSearch(chatID int64, query, mode string) {
	query = strings.TrimSpace(query)
	if len([]rune(query)) < 2 {
		b.send(chatID, "برای جست‌وجو حداقل ۲ حرف بنویس (مثلاً Qwen).")
		return
	}
	if mode == "" {
		mode = "model"
	}
	b.setScx(chatID, searchCtx{query: query, mode: mode})
	hits, offline := b.searchProvHits(query)
	if len(hits) == 0 {
		b.send(chatID, "برای «"+query+"» چیزی پیدا نشد.\nمی‌توانی با ✍️ تایپ دستی به‌صورت provider/model بفرستی.")
		return
	}
	if offline {
		b.send(chatID, "⚠️ کاتالوگ آنلاین در دسترس نبود؛ نتیجه فقط از فهرست داخلی است.")
	}
	b.renderProvScreen(chatID, 0, mode, hits, 0, true)
}

func (b *Bot) openSearchModel(chatID int64, msgID int, pid string) {
	sc, ok := b.getScx(chatID)
	filter := ""
	if ok && sc.mode != "key" {
		filter = sc.query
	}
	b.showModelsFor(chatID, msgID, pid, filter)
}

// ---------- صفحهٔ پروایدرها ----------

func (b *Bot) openProvFull(chatID int64, msgID int, mode string, page int) {
	b.renderProvScreen(chatID, msgID, mode, b.provItems(), page, false)
}

func (b *Bot) openProvSearch(chatID int64, msgID int, page int) {
	sc, ok := b.getScx(chatID)
	if !ok {
		return
	}
	hits, _ := b.searchProvHits(sc.query)
	b.renderProvScreen(chatID, msgID, sc.mode, hits, page, true)
}

func (b *Bot) askProviderSearch(chatID int64, msgID int, mode string) {
	text := "🔍 اسم پروایدر یا مدل را بنویس (مثلاً Qwen، Hetzner، deepseek)."
	if mode == "key" {
		text = "🔍 اسم پروایدری که می‌خواهی کلیدش را بگذاری بنویس."
	}
	text += "\n(برای انصراف: /cancel)"
	b.editSettings(chatID, msgID, text, rowsOf([]tgbotapi.InlineKeyboardButton{inlineBtn("❌ انصراف", "s:main")}))
	b.setPendingByChat(chatID, "ps:"+mode)
}

func (b *Bot) askFilterModels(chatID int64, msgID int, pid string) {
	label := pid
	if p, ok := presetByID(pid); ok {
		label = p.label
	}
	b.editSettings(chatID, msgID,
		"🔍 بخشی از نام مدل از <b>"+clipHead(label, 40)+"</b> را بنویس (مثلاً qwen).\n(برای انصراف: /cancel)",
		rowsOf([]tgbotapi.InlineKeyboardButton{inlineBtn("❌ انصراف", "s:model")}))
	b.setPendingByChat(chatID, "mf:"+pid)
}
