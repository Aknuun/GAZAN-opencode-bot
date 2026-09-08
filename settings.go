package main

import (
	"fmt"
	"strconv"
	"strings"
	"time"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// ---------- پروایدرهای پیشنهادی ----------

type presetProvider struct {
	id     string
	label  string
	models []string
}

var presetProviders = []presetProvider{
	{"deepseek", "دیپ‌سیک (deepseek)", []string{"deepseek-v4-flash", "deepseek-v4-pro", "deepseek-v4-flash-vision-exp"}},
	{"openai", "OpenAI", []string{"gpt-5", "gpt-5-mini", "gpt-5-nano", "gpt-4o", "gpt-4o-mini"}},
	{"anthropic", "Anthropic (Claude)", []string{"claude-sonnet-4-5", "claude-opus-4-5", "claude-haiku-4-5"}},
	{"google", "Google (Gemini)", []string{"gemini-2.5-pro", "gemini-2.5-flash", "gemini-2.5-flash-lite"}},
	{"xai", "xAI (Grok)", []string{"grok-4", "grok-4-fast"}},
	{"openrouter", "OpenRouter", []string{"auto"}},
}

func presetByID(id string) (presetProvider, bool) {
	for _, p := range presetProviders {
		if p.id == id {
			return p, true
		}
	}
	return presetProvider{}, false
}

func (b *Bot) providerLabel(env *ocEnv, id string) string {
	label := id
	if p, ok := presetByID(id); ok {
		label = p.label
	}
	if env.hasKey(id) {
		return label + " ✅"
	}
	return label
}

func (b *Bot) envFor() *ocEnv {
	if b.cfg == nil {
		return newOCEnv(&Config{})
	}
	return newOCEnv(b.cfg)
}

// ---------- صفحه اصلی تنظیمات ----------

func (b *Bot) settingsSummary(env *ocEnv, userID, chatID int64) string {
	model := env.currentModel()
	var sb strings.Builder
	sb.WriteString("⚙️ <b>تنظیمات سرور opencode</b>\n\n")
	if model == "" {
		sb.WriteString("🧠 مدل: <i>تنظیم نشده</i>\n")
	} else {
		sb.WriteString("🧠 مدل: <code>" + model + "</code>\n")
	}
	pid := ""
	if i := strings.IndexByte(model, '/'); i > 0 {
		pid = model[:i]
	}
	providers := env.providersConfigured()
	if len(providers) == 0 {
		sb.WriteString("🔑 کلید API: <i>هیچ کلیدی ست نشده</i>\n")
	} else {
		has := env.hasKey(pid)
		state := ""
		if model != "" && !has {
			state = " ⚠️ مدل فعلی کلید ندارد"
		}
		sb.WriteString("🔑 کلیدهای ست‌شده: <code>" + strings.Join(providers, ", ") + "</code>" + state + "\n")
	}
	sb.WriteString("🤖 agent: <code>" + b.agentForCurrent(userID, chatID) + "</code>\n")
	if env.Service != "" {
		sb.WriteString("🛠 سرویس: <code>" + env.Service + "</code>\n")
	} else {
		sb.WriteString("🛠 سرویس: <i>شناسایی نشد</i>\n")
	}
	sb.WriteString("\nتغییرات مستقیم روی کانفیگ خود opencode اعمال و سرور ری‌استارت می‌شود.")
	return sb.String()
}

func (b *Bot) agentForCurrent(userID, chatID int64) string {
	if st := b.stateFor(userID, chatID); st != nil && st.Agent != "" {
		return st.Agent
	}
	if b.cfg != nil && b.cfg.Agent != "" {
		return b.cfg.Agent
	}
	return "build"
}

func (b *Bot) agentOptions(env *ocEnv) []string {
	base := []string{"build", "general", "plan"}
	seen := map[string]bool{}
	var out []string
	for _, a := range append(base, env.customAgents()...) {
		if a == "" || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

func (b *Bot) openSettings(userID, chatID int64) {
	env := b.envFor()
	msg := tgbotapi.NewMessage(chatID, b.settingsSummary(env, userID, chatID))
	msg.ParseMode = "HTML"
	msg.ReplyMarkup = settingsMainMarkup()
	b.api.Send(msg)
}

func (b *Bot) editSettings(chatID int64, msgID int, text string, kb *tgbotapi.InlineKeyboardMarkup) {
	edit := tgbotapi.NewEditMessageText(chatID, msgID, text)
	edit.ParseMode = "HTML"
	if kb != nil {
		edit.ReplyMarkup = kb
	}
	b.api.Send(edit)
}

func inlineBtn(text, data string) tgbotapi.InlineKeyboardButton {
	return tgbotapi.InlineKeyboardButton{Text: text, CallbackData: &data}
}

func rowsOf(rows ...[]tgbotapi.InlineKeyboardButton) *tgbotapi.InlineKeyboardMarkup {
	return &tgbotapi.InlineKeyboardMarkup{InlineKeyboard: rows}
}

func settingsMainMarkup() *tgbotapi.InlineKeyboardMarkup {
	return rowsOf(
		[]tgbotapi.InlineKeyboardButton{inlineBtn("🧠 تغییر مدل", "s:model"), inlineBtn("🤖 agent", "s:ag")},
		[]tgbotapi.InlineKeyboardButton{inlineBtn("🔑 کلید API", "s:key")},
		[]tgbotapi.InlineKeyboardButton{inlineBtn("🔄 ری‌استارت سرور", "s:restart")},
		[]tgbotapi.InlineKeyboardButton{inlineBtn("❌ بستن", "s:close")},
	)
}

// ---------- کلیک روی دکمه‌ها ----------

func (b *Bot) onSettingsCallback(userID, chatID int64, msgID int, data string) {
	env := b.envFor()
	switch {
	case data == "s:model":
		b.showProviderPicker(chatID, msgID, "model")
	case data == "s:key":
		b.showProviderPicker(chatID, msgID, "key")
	case data == "s:ag":
		b.showAgentPicker(chatID, msgID)
	case data == "s:restart":
		b.editSettings(chatID, msgID, "🔄 در حال ری‌استارت…", nil)
		if err := env.restart(); err != nil {
			b.editSettings(chatID, msgID, "❌ "+err.Error()+"\n\nسشن فعلی را با /new عوض کن یا سرور را دستی ری‌استارت کن.", settingsMainMarkup())
			return
		}
		time.Sleep(1500 * time.Millisecond)
		b.editSettings(chatID, msgID, "✅ سرور opencode ری‌استارت شد.", settingsMainMarkup())
	case data == "s:close":
		edit := tgbotapi.NewEditMessageText(chatID, msgID, "بسته شد.")
		b.api.Send(edit)
	case data == "s:main":
		b.editSettings(chatID, msgID, b.settingsSummary(env, userID, chatID), settingsMainMarkup())
	default:
		parts := strings.Split(data, ":")
		if len(parts) < 3 || parts[0] != "s" {
			return
		}
		switch parts[1] {
		case "pv":
			// s:pv:<pid>
			b.showModelsFor(chatID, msgID, parts[2])
		case "sel":
			// s:sel:<pid>:<idx>
			if len(parts) == 4 {
				b.applyPresetModelIdx(chatID, msgID, parts[2], parts[3])
			}
		case "mm":
			b.requestPendingModel(chatID, msgID, parts[2])
		case "kp":
			b.requestKey(chatID, msgID, parts[2])
		case "krm":
			b.removeKey(chatID, msgID, parts[2])
		case "ag":
			b.setAgentFromCallback(userID, chatID, msgID, parts[2])
		}
	}
}

func (b *Bot) showProviderPicker(chatID int64, msgID int, mode string) {
	env := b.envFor()
	var sb strings.Builder
	switch mode {
	case "key":
		sb.WriteString("🔑 برای کدام پروایدر کلید API می‌گذاری؟\nبعد از انتخاب، کلید را تایپ و ارسال کن.")
	case "krm":
		sb.WriteString("🗑 کلید کدام پروایدر حذف شود؟")
	default:
		sb.WriteString("🧠 مدل کدام پروایدر را می‌خواهی؟")
	}
	sb.WriteString("\n\n(✅ یعنی کلیدش ست شده)")
	var rows [][]tgbotapi.InlineKeyboardButton
	var row []tgbotapi.InlineKeyboardButton
	add := func(id string) {
		tok := "s:pv:" + id
		switch mode {
		case "key":
			tok = "s:kp:" + id
		case "krm":
			tok = "s:krm:" + id
		}
		row = append(row, inlineBtn(b.providerLabel(env, id), tok))
		if len(row) == 2 {
			rows = append(rows, row)
			row = nil
		}
	}
	for _, p := range presetProviders {
		add(p.id)
	}
	for _, pid := range env.providersConfigured() {
		if _, isPreset := presetByID(pid); !isPreset {
			add(pid)
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	if mode == "model" {
		rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn("✍️ تایپ دستی (provider/model)", "s:mm:__manual__")})
	}
	rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn("🔙", "s:main")})
	b.editSettings(chatID, msgID, sb.String(), rowsOf(rows...))
}

func (b *Bot) showModelsFor(chatID int64, msgID int, pid string) {
	env := b.envFor()
	var sb strings.Builder
	var rows [][]tgbotapi.InlineKeyboardButton
	if p, ok := presetByID(pid); ok {
		sb.WriteString("🧠 مدل‌های <b>" + p.label + "</b>:")
		for i, m := range p.models {
			label := m
			if env.currentModel() == pid+"/"+m {
				label += " ← فعلی"
			}
			rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn(label, "s:sel:"+pid+":"+strconv.Itoa(i))})
		}
	} else {
		sb.WriteString("🧠 پروایدر <b>" + pid + "</b> در لیست پیشنهادی نیست؛ مدل را دستی تایپ کن.")
	}
	sb.WriteString("\n\nاگر مدل‌ات در لیست نبود، «تایپ دستی» را بزن.")
	rows = append(rows,
		[]tgbotapi.InlineKeyboardButton{inlineBtn("✍️ تایپ دستی", "s:mm:"+pid)},
		[]tgbotapi.InlineKeyboardButton{inlineBtn("🔙", "s:model")},
	)
	b.editSettings(chatID, msgID, sb.String(), rowsOf(rows...))
}

func (b *Bot) applyPresetModelIdx(chatID int64, msgID int, pid, idxStr string) {
	idx, err := strconv.Atoi(idxStr)
	if err != nil {
		return
	}
	p, ok := presetByID(pid)
	if !ok || idx < 0 || idx >= len(p.models) {
		return
	}
	b.applyModel(chatID, msgID, pid+"/"+p.models[idx])
}

func (b *Bot) requestPendingModel(chatID int64, msgID int, pid string) {
	env := b.envFor()
	text := "✍️ نام کامل مدل را بفرست.\nقالب: <code>provider/model</code>\nمثل: <code>deepseek/deepseek-v4-flash</code>"
	if pid != "" && pid != "__manual__" {
		label := pid
		if p, ok := presetByID(pid); ok {
			label = p.label
		}
		text = "✍️ نام مدل از پروایدر <b>" + label + "</b> را بفرست."
		if p, ok := presetByID(pid); ok && len(p.models) > 0 {
			text += "\nمثلاً: <code>" + pid + "/" + p.models[0] + "</code>"
		}
	}
	if cur := env.currentModel(); cur != "" {
		text += "\n\nمدل فعلی: <code>" + cur + "</code>"
	}
	b.editSettings(chatID, msgID, text, rowsOf([]tgbotapi.InlineKeyboardButton{inlineBtn("❌ انصراف", "s:main")}))
	b.setPendingByChat(chatID, "model:"+pid)
}

func (b *Bot) requestKey(chatID int64, msgID int, pid string) {
	env := b.envFor()
	text := "🔑 کلید API پروایدر <b>" + b.providerLabel(env, pid) + "</b> را بفرست."
	if env.hasKey(pid) {
		text += "\n(کلید قبلی دارد؛ با ارسال کلید جدید جایگزین می‌شود.)"
	}
	b.editSettings(chatID, msgID, text, rowsOf([]tgbotapi.InlineKeyboardButton{inlineBtn("❌ انصراف", "s:main")}))
	b.setPendingByChat(chatID, "key:"+pid)
}

func (b *Bot) removeKey(chatID int64, msgID int, pid string) {
	env := b.envFor()
	if err := env.removeAuthKey(pid); err != nil {
		b.editSettings(chatID, msgID, "❌ حذف ناموفق: "+err.Error(), settingsMainMarkup())
		return
	}
	b.editSettings(chatID, msgID, "🗑 کلید <code>"+pid+"</code> حذف شد.", settingsMainMarkup())
}

func (b *Bot) showAgentPicker(chatID int64, msgID int) {
	env := b.envFor()
	sb := "🤖 agent موردنظر را انتخاب کن:"
	var rows [][]tgbotapi.InlineKeyboardButton
	var row []tgbotapi.InlineKeyboardButton
	for _, a := range b.agentOptions(env) {
		row = append(row, inlineBtn(a, "s:ag:"+a))
		if len(row) == 3 {
			rows = append(rows, row)
			row = nil
		}
	}
	if len(row) > 0 {
		rows = append(rows, row)
	}
	rows = append(rows, []tgbotapi.InlineKeyboardButton{inlineBtn("🔙", "s:main")})
	b.editSettings(chatID, msgID, sb, rowsOf(rows...))
}

func (b *Bot) setAgentFromCallback(userID, chatID int64, msgID int, agent string) {
	if st := b.stateFor(userID, chatID); st != nil {
		st.Agent = agent
		b.saveStates()
	}
	b.editSettings(chatID, msgID, "✅ agent فعال: <code>"+agent+"</code>", settingsMainMarkup())
}

func (b *Bot) applyModel(chatID int64, msgID int, full string) {
	env := b.envFor()
	if err := env.setModel(full); err != nil {
		b.editSettings(chatID, msgID, "❌ "+err.Error(), settingsMainMarkup())
		return
	}
	pid := strings.SplitN(full, "/", 2)[0]
	if !env.hasKey(pid) {
		b.editSettings(chatID, msgID,
			"✅ مدل روی <code>"+full+"</code> ست شد؛ ولی پروایدر <code>"+pid+"</code> کلید API ندارد.\nاز «🔑 کلید API» کلید را بفرست.",
			rowsOf(
				[]tgbotapi.InlineKeyboardButton{inlineBtn("🔑 ست کردن کلید", "s:key")},
				[]tgbotapi.InlineKeyboardButton{inlineBtn("🔙", "s:main")},
			))
		b.restartAfter(chatID)
		return
	}
	b.editSettings(chatID, msgID, "✅ مدل روی <code>"+full+"</code> ست شد.", settingsMainMarkup())
	b.restartAfter(chatID)
}

func (b *Bot) restartAfter(chatID int64) {
	env := b.envFor()
	if err := env.restart(); err != nil {
		b.send(chatID, "⚠️ ری‌استارت خودکار نشد؛ /new بزن یا سرور را دستی ری‌استارت کن.")
		return
	}
	b.send(chatID, "✅ سرور opencode ری‌استارت شد و تغییرات اعمال شد.")
}

// ---------- pending (تایپ متنی) ----------

func (b *Bot) setPendingByChat(chatID int64, pending string) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for _, st := range b.states {
		if st.ChatID == chatID {
			st.Pending = pending
			b.saveStates()
			return
		}
	}
}

func (b *Bot) clearPending(st *UserState) {
	b.mu.Lock()
	defer b.mu.Unlock()
	st.Pending = ""
	b.saveStates()
}

func (b *Bot) handlePending(userID, chatID int64, pending, text string) {
	text = strings.TrimSpace(text)
	env := b.envFor()
	st := b.stateFor(userID, chatID)
	defer b.clearPending(st)
	parts := strings.SplitN(pending, ":", 2)
	kind := parts[0]
	arg := ""
	if len(parts) > 1 {
		arg = parts[1]
	}
	switch kind {
	case "rn":
		// تغییر نام (عنوان فارسی) یک نشست
		sid := arg
		if sid == "" {
			b.send(chatID, "نشست مشخص نیست.")
			return
		}
		b.mu.Lock()
		st2 := b.states[userID]
		if st2 != nil {
			if st2.Labels == nil {
				st2.Labels = map[string]string{}
			}
			st2.Labels[sid] = text
			b.saveStates()
		}
		b.mu.Unlock()
		b.send(chatID, "✅ نام نشست عوض شد:\n"+text)
	case "model":
		full := text
		if !strings.Contains(full, "/") && arg != "" && arg != "__manual__" {
			full = arg + "/" + text
		}
		if !strings.Contains(full, "/") {
			b.send(chatID, "قالب باید provider/model باشد (مثل deepseek/deepseek-v4-flash).")
			return
		}
		msg := tgbotapi.NewMessage(chatID, "⏳ در حال تنظیم…")
		m, _ := b.api.Send(msg)
		b.applyModel(chatID, m.MessageID, full)
	case "key":
		pid := arg
		if pid == "" {
			b.send(chatID, "پروایدر مشخص نیست.")
			return
		}
		if err := env.addAuthKey(pid, text); err != nil {
			b.send(chatID, "❌ "+err.Error())
			return
		}
		cur := env.currentModel()
		note := "\nبرای استفاده، مدل این پروایدر را ست کن."
		if strings.HasPrefix(cur, pid+"/") {
			note = "\nمدل فعلی از همین پروایدر است؛ آمادهٔ استفاده است. ✅"
		}
		b.send(chatID, "🔑 کلید <code>"+pid+"</code> ست شد."+note)
		b.restartAfter(chatID)
	}
}

// ---------- آمادگی سرور ----------

func (b *Bot) ocReady() bool {
	env := b.envFor()
	m := env.currentModel()
	if m == "" {
		return false
	}
	pid := strings.SplitN(m, "/", 2)[0]
	return env.hasKey(pid)
}

func (b *Bot) ocSetupHint() string {
	return fmt.Sprintf("سرور opencode هنوز مدل و کلید API ندارد. از دکمهٔ %s تنظیمش کن.", btnSettings)
}
