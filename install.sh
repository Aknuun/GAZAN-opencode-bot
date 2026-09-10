#!/usr/bin/env bash
#
# GAZAN-opencode-bot (opencode-tg-bot) — نصب / آپدیت روی سرور خودت
#
# اجرا:
#   از داخل پوشه پروژه:            sudo bash install.sh
#   یا یک‌خطی از گیت‌هاب:
#     curl -fsSL https://raw.githubusercontent.com/Aknuun/GAZAN-opencode-bot/master/install.sh -o /tmp/opencode-tg-install.sh \
#       && sudo bash /tmp/opencode-tg-install.sh
#
#   ⚠️ با «sudo bash <(curl …)» اجرا نکن: sudo فایل‌دسکریپتورِ process
#   substitution را نمی‌دهد و اجرا با «/dev/fd/63: No such file or directory»
#   متوقف می‌شود. همین‌طور اسکریپتِ interactive را از لولهٔ «curl | sudo bash»
#   اجرا نکن، چون stdin دیگر ترمینال نیست و پرسش‌ها خوانده نمی‌شوند.
#   (خودِ اسکریپت در این حالت از /dev/tty می‌خواند؛ ولی راهِ امن همان دانلود است.)
#
# اگر قبلاً نصب شده باشد، به‌جای نصبِ دوباره، «آپدیت» انجام می‌دهد
# (دریافت آخرین نسخه + build + ری‌استارت) بدون اینکه توکن/آیدی را دوباره بپرسد.
# برای نصبِ غیرتعاملی (بدون ترمینال) مقادیر را از قبل با متغیر محیطی ست کن:
#   TELEGRAM_BOT_TOKEN=... ALLOWED_USER_IDS=... sudo bash install.sh
#
set -euo pipefail

c_ok()  { printf '\033[32m%s\033[0m\n' "$*"; }
c_warn(){ printf '\033[33m%s\033[0m\n' "$*"; }
c_err() { printf '\033[31m%s\033[0m\n' "$*"; }
die(){ c_err "$*"; exit 1; }

APP="opencode-tg-bot"
REPO="${OPENCODE_TG_REPO:-Aknuun/GAZAN-opencode-bot}"
SVC_OC="opencode-serve.service"
SVC_BOT="${APP}.service"
TARGET="${OPENCODE_TG_TARGET:-/root/$APP}"

[[ $EUID -eq 0 ]] || die "لطفاً با root اجرا کن:  sudo bash install.sh"
command -v curl >/dev/null 2>&1 || die "curl نصب نیست؛ اول:  apt update && apt install -y curl"
command -v systemctl >/dev/null 2>&1 || die "systemd پیدا نشد."

# ---------- تشخیص نصب قبلی ----------
installed() {
  systemctl list-unit-files --no-legend "$SVC_BOT" 2>/dev/null | grep -q "$SVC_BOT"
}
MODE="install"
installed && MODE="update"

# ---------- پیدا کردن/دریافت پروژه ----------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd)"
if [[ -f "$SCRIPT_DIR/main.go" ]]; then
  SRC="$SCRIPT_DIR"
  c_ok "پوشه پروژه (محلی): $SRC"
elif [[ -f "$TARGET/main.go" ]]; then
  SRC="$TARGET"
  c_ok "پوشه پروژه: $SRC"
else
  SRC="$TARGET"
  c_warn "دریافت سورس از گیت‌هاب ($REPO)…"
  mkdir -p "$SRC"
  curl -fsSL "https://github.com/$REPO/archive/refs/heads/master.tar.gz" -o /tmp/$APP-src.tar.gz
  tar xzf /tmp/$APP-src.tar.gz -C "$SRC" --strip-components=1
  rm -f /tmp/$APP-src.tar.gz
fi

# در حالت آپدیت، اگر سورس محلیِ جدا داریم، آخرین نسخه را می‌گیریم
if [[ "$MODE" == "update" && "${OPENCODE_TG_SKIP_FETCH:-0}" != "1" && ! -f "$SCRIPT_DIR/main.go" ]]; then
  c_warn "دریافت آخرین نسخه…"
  curl -fsSL "https://github.com/$REPO/archive/refs/heads/master.tar.gz" -o /tmp/$APP-src.tar.gz
  tar xzf /tmp/$APP-src.tar.gz -C "$SRC" --strip-components=1
  rm -f /tmp/$APP-src.tar.gz
fi

ENV_FILE="$SRC/.env"
read_env() { # key -> مقدار (بدون مقدار پیش‌فرض)
  grep -E "^${1}=" "$ENV_FILE" 2>/dev/null | head -1 | cut -d= -f2- | tr -d '"' || true
}

# ---------- اطلاعات پایه ----------
# اولویت: متغیر محیطی > فایل .env  (این‌طوری می‌شود غیرتعاملی هم نصب کرد)
BOT_TOKEN="${TELEGRAM_BOT_TOKEN:-$(read_env TELEGRAM_BOT_TOKEN)}"
ALLOWED="${ALLOWED_USER_IDS:-$(read_env ALLOWED_USER_IDS)}"
PORT="${OPENCODE_TG_PORT:-$(read_env OPENCODE_BASE_URL | sed -E 's|.*:([0-9]+)$|\1|' || true)}"

ask() { # $1: نام متغیر   $2: پیام — تا پر شدن مقدار می‌پرسد.
  # اگر stdin ترمینال نیست (مثل curl … | sudo bash) از /dev/tty می‌خواند؛
  # اگر اصلاً ترمینالی در دسترس نبود، با پیامِ واضح می‌ایستد به‌جای چرخش بی‌نهایت.
  local var="$1" label="$2"
  while [[ -z "${!var:-}" ]]; do
    if [[ -e /dev/tty ]]; then
      if ! read -r -p "$label " "$var" < /dev/tty; then
        c_err "خواندن از ترمینال ممکن نشد (stdin بسته است)."
        die "برای نصبِ بدون ترمینال، مقدار را از قبل ست کن:  ${var}=مقدار  سپس  sudo bash install.sh"
      fi
    else
      c_err "ترمینال در دسترس نیست (stdin بسته است)."
      die "برای نصبِ بدون ترمینال، مقدار را از قبل ست کن:  ${var}=مقدار  سپس  sudo bash install.sh"
    fi
  done
}

if [[ "$MODE" == "install" ]]; then
  c_ok "== نصب جدید =="
  ask BOT_TOKEN "① توکن ربات تلگرام (از @BotFather بگیر):"
  ok="$(curl -fsS --max-time 15 "https://api.telegram.org/bot${BOT_TOKEN}/getMe" 2>/dev/null | grep -o '"username":"[^"]*"' || true)"
  if [[ -z "$ok" ]]; then
    c_err "توکن معتبر نیست (تلگرام جواب نداد)."
    exit 1
  fi
  c_ok "توکن تأیید شد: $ok"

  ask ALLOWED "② آیدی عددی تلگرام کاربران مجاز (چندتا با کاما؛ از @userinfobot):"
  c_ok "کاربران مجاز: $ALLOWED"
  PORT="${PORT:-17890}"
else
  c_ok "== آپدیت (نصب قبلی پیدا شد) =="
  [[ -z "$BOT_TOKEN" ]] && die "فایل .env معتبر نیست؛ آن را دستی چک کن."
  [[ -z "$ALLOWED" ]] && ALLOWED="$(read_env ALLOWED_USER_IDS)"
  PORT="${PORT:-17890}"
  c_ok "نگه‌داشتن تنظیمات قبلی (توکن/کاربران/پورت)"
fi

BASE_URL="http://127.0.0.1:$PORT"
AGENT="$(read_env OPENCODE_AGENT)"; AGENT="${AGENT:-build}"
STATE="$SRC/state.json"

# ---------- opencode ----------
OPENCODE_BIN="$(command -v opencode || true)"
if [[ -z "$OPENCODE_BIN" ]]; then
  c_warn "opencode نصب نیست؛ در حال نصب…"
  curl -fsSL https://opencode.ai/install | bash || true
  for cand in "$HOME/.opencode/bin/opencode" /usr/local/bin/opencode "$HOME/.local/bin/opencode"; do
    if [[ -x "$cand" ]]; then OPENCODE_BIN="$cand"; break; fi
  done
  [[ -z "$OPENCODE_BIN" ]] && die "نصب opencode ناموفق بود."
fi
c_ok "opencode: $OPENCODE_BIN"

# ---------- .env (فقط در نصب جدید نوشته می‌شود) ----------
if [[ "$MODE" == "install" ]]; then
  cat > "$ENV_FILE" <<EOF
TELEGRAM_BOT_TOKEN=$BOT_TOKEN
ALLOWED_USER_IDS=$ALLOWED
OPENCODE_BASE_URL=$BASE_URL
OPENCODE_AGENT=$AGENT
STATE_FILE=$STATE
EOF
  chmod 600 "$ENV_FILE"
  c_ok ".env ساخته شد: $ENV_FILE"
  c_warn "نکته: مدل و کلید API را بعداً داخل ربات (دکمهٔ ⚙️ تنظیمات) ست می‌کنی."
else
  # مطمئن شو STATE_FILE درست است
  if ! grep -q "^STATE_FILE=" "$ENV_FILE"; then
    echo "STATE_FILE=$STATE" >> "$ENV_FILE"
  fi
fi

# ---------- باینری ----------
BIN="$SRC/$APP"
if ! command -v go >/dev/null 2>&1; then
  c_warn "Go پیدا نشد؛ نصب golang…"
  apt-get update -qq >/dev/null 2>&1 || true
  apt-get install -y golang-go >/dev/null 2>&1 || die "نصب golang ناموفق بود."
fi
c_warn "build از سورس…"
(cd "$SRC" && go build -o "$APP" .) || die "go build ناموفق بود."
[[ -x "$BIN" ]] || die "باینری ساخته نشد."
c_ok "باینری: $BIN"

# ---------- سرویس‌ها ----------
cat > "/etc/systemd/system/$SVC_OC" <<EOF
[Unit]
Description=OpenCode headless server (backend for $APP)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=/root
Environment=HOME=/root
ExecStart=$OPENCODE_BIN serve --port $PORT --hostname 127.0.0.1 --print-logs
ExecStartPost=/bin/bash -lc 'for i in {1..120}; do : > /dev/tcp/127.0.0.1/$PORT 2>/dev/null && exit 0; sleep 0.25; done; echo "opencode serve not listening on 127.0.0.1:$PORT after wait" >&2; exit 1'
Restart=always
RestartSec=5
Environment=OPENCODE_DISABLE_AUTOUPDATE=true

[Install]
WantedBy=multi-user.target
EOF

cat > "/etc/systemd/system/$SVC_BOT" <<EOF
[Unit]
Description=OpenCode Telegram Bot bridge
After=network-online.target opencode-serve.service
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=$SRC
Environment=HOME=/root
EnvironmentFile=$ENV_FILE
ExecStart=$BIN
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now "$SVC_OC" "$SVC_BOT" >/dev/null 2>&1 || true
systemctl restart "$SVC_OC" "$SVC_BOT"
c_ok "سرویس‌ها ری‌استارت شدند: $SVC_OC و $SVC_BOT"

sleep 2
echo "------------------------------------------------------------"
if [[ "$MODE" == "update" ]]; then
  c_ok "آپدیت کامل شد."
else
  c_ok "نصب کامل شد."
  echo "  در تلگرام به ربات خودت /start بزن."
  echo "  اول دکمهٔ ⚙️ تنظیمات را بزن و «🔑 کلید API» و سپس «🧠 تغییر مدل» را ست کن."
fi
echo "  لاگ‌ها: journalctl -u $SVC_BOT -f"
echo "------------------------------------------------------------"
