# GAZAN-opencode-bot (opencode-tg-bot)

پل تلگرام برای [opencode](https://opencode.ai) روی سرور خودت.
ربات تلگرام را به یک سرور headless «opencode» وصل می‌کند تا هر جا هستی با دستیار هوش مصنوعی سرورت گفتگو کنی، فایل بفرستی و مصرف/هزینه را ببینی.

A Telegram bridge for a headless opencode server running on your own VPS.

## امکانات

- گفتگوی زنده با opencode (پاسخ به‌صورت live روی همان پیام به‌روز می‌شود)
- ارسال فایل/عکس و تحلیل آن
- چند agent: `build` · `general` · `plan` (+ agentهای سفارشی)
- **⚙️ تنظیمات داخل ربات**:
  - تغییر **مدل** و **پروایدر** (DeepSeek، OpenAI، Claude، Gemini، Groq و…) دقیقاً روی کانفیگ خود opencode
  - تنظیم / تعویض **کلید API** و ری‌استارت خودکار سرور
  - انتخاب agent
- صف فرمان‌ها + توقف عملیات (`⏹`)
- نمایش هزینه و توکن هر session (`📊`)
- دسترسی فقط برای کاربران مجاز (allow-list)

## نیازمندی‌ها

- سرور لینوکس (Ubuntu/Debian توصیه می‌شود) با systemd و root
- توکن ربات تلگرام از [@BotFather](https://t.me/BotFather)
- آیدی عددی تلگرام خودت از [@userinfobot](https://t.me/userinfobot)
- کلید API یک مدل‌پروایدر (مثل DeepSeek/OpenAI) — داخل ربات ست می‌شود

## نصب (یک‌دستوری)

```bash
sudo bash <(curl -fsSL https://raw.githubusercontent.com/Aknuun/GAZAN-opencode-bot/main/install.sh)
```

نصب‌کننده:
1. توکن ربات را می‌پرسد (اول)
2. آیدی عددی کاربران مجاز را می‌پرسد (دوم)
3. در صورت نبودن، خودش opencode و Go را نصب می‌کند
4. سرویس‌های systemd را می‌سازد و روشن می‌کند

سپس در تلگرام به ربات خودت `/start` بزن، دکمهٔ **⚙️ تنظیمات** را بزن و «🔑 کلید API» و «🧠 تغییر مدل» را ست کن — ربات ری‌استارت می‌شود و آماده است.

## اجرای دستی (توسعه)

```bash
# روی سرور: سرور opencode را روشن کن
opencode serve --port 17890 --hostname 127.0.0.1 --print-logs &

# سپس پل را اجرا کن
cp .env.example .env   # و مقادیر را پر کن
go build -o opencode-tg-bot .
./opencode-tg-bot
```

## متغیرهای محیطی

| متغیر | توضیح |
|---|---|
| `TELEGRAM_BOT_TOKEN` | توکن ربات تلگرام (اجباری) |
| `ALLOWED_USER_IDS` | آیدی‌های عددی مجاز، با کاما (اجباری) |
| `OPENCODE_BASE_URL` | آدرس سرور opencode (پیش‌فرض `http://127.0.0.1:14999`) |
| `OPENCODE_AGENT` | agent پیش‌فرض (پیش‌فرض `build`) |
| `STATE_FILE` | مسیر ذخیرهٔ sessionها |
| `OPENCODE_CONFIG_HOME` | پوشه کانفیگ opencode (برای تنظیمات؛ خودکار اگر خالی باشد) |
| `OPENCODE_DATA_HOME` | پوشه دیتای opencode شامل `auth.json` (خودکار اگر خالی باشد) |
| `OPENCODE_SERVE_SERVICE` | نام سرویس systemd سرور opencode برای ری‌استارت خودکار |

## دستورات ربات

```
/new         شروع session تازه
/use <id>    ادامهٔ session قبلی
/list        فهرست sessionهای اخیر
/status      جزئیات و هزینه
/agent       نمایش/تغییر agent
/queue       تعداد موارد صف
/flush       پاک کردن صف
/cancel      توقف اجرای جاری
/help        راهنما
```

## امنیت

- سرور opencode فقط روی `127.0.0.1` گوش می‌دهد.
- فقط کاربران داخل `ALLOWED_USER_IDS` می‌توانند با ربات حرف بزنند.
- `.env` و `state.json` نباید جایی عمومی منتشر شوند (در `.gitignore` هستند).
- کلیدهای API در `auth.json` خود opencode نگهداری می‌شوند؛ تعویضشان از داخل ربات امن است.
