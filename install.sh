#!/usr/bin/env bash
#
# opencode-tg-bot — نصب روی سرور خودت (Ubuntu/Debian با systemd)
#
# اجرا:
#   از داخل پوشه پروژه:            sudo bash install.sh
#   یا یک‌خطی از گیت‌هاب:          bash <(curl -fsSL <URL خام install.sh>)
#
set -euo pipefail

c_ok()  { printf '\033[32m%s\033[0m\n' "$*"; }
c_warn(){ printf '\033[33m%s\033[0m\n' "$*"; }
c_err() { printf '\033[31m%s\033[0m\n' "$*"; }
die(){ c_err "$*"; exit 1; }

APP="opencode-tg-bot"
SVC_OC="opencode-serve.service"
SVC_BOT="${APP}.service"

[[ $EUID -eq 0 ]] || die "لطفاً با root اجرا کن:  sudo bash install.sh"
command -v curl >/dev/null 2>&1 || die "curl نصب نیست؛ اول:  apt update && apt install -y curl"
command -v systemctl >/dev/null 2>&1 || die "systemd پیدا نشد."

# ---------- پیدا کردن محل پروژه ----------
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" 2>/dev/null && pwd)"
TARGET="${OPENCODE_TG_TARGET:-/root/$APP}"
if [[ -f "$SCRIPT_DIR/main.go" ]]; then
  SRC="$SCRIPT_DIR"
elif [[ -f "$TARGET/main.go" ]]; then
  SRC="$TARGET"
else
  # حالت یک‌خطی: پروژه در محل نیست؛ از گیت‌هاب می‌گیریم
  REPO="${OPENCODE_TG_REPO:-Aknuun/GAZAN-opencode-bot}"
  if [[ -z "$REPO" ]]; then
    die "این اسکریپت باید داخل پوشهٔ پروژه اجرا شود، یا OPENCODE_TG_REPO ست شود."
  fi
  c_warn "در حال دریافت سورس از گیت‌هاب ($REPO)…"
  mkdir -p "$TARGET"
  curl -fsSL "https://github.com/$REPO/archive/refs/heads/main.tar.gz" -o /tmp/$APP-src.tar.gz
  tar xzf /tmp/$APP-src.tar.gz -C "$TARGET" --strip-components=1
  SRC="$TARGET"
  rm -f /tmp/$APP-src.tar.gz
fi
c_ok "پوشه پروژه: $SRC"

# ---------- توکن ربات (اول) ----------
ENV_FILE="$SRC/.env"
BOT_TOKEN=""
if [[ -f "$ENV_FILE" ]]; then
  BOT_TOKEN="$(grep -E '^TELEGRAM_BOT_TOKEN=' "$ENV_FILE" | cut -d= -f2- | tr -d '"' || true)"
fi
while [[ -z "$BOT_TOKEN" ]]; do
  read -r -p "① توکن ربات تلگرام (از @BotFather بگیر): " BOT_TOKEN
done
ok="$(curl -fsS --max-time 15 "https://api.telegram.org/bot${BOT_TOKEN}/getMe" 2>/dev/null | grep -o '"username":"[^"]*"' || true)"
if [[ -z "$ok" ]]; then
  c_err "توکن معتبر نیست (تلگرام جواب نداد). دوباره از @BotFather چک کن."
  exit 1
fi
c_ok "توکن تأیید شد: $ok"

# ---------- آیدی عددی مجاز (دوم) ----------
ALLOWED=""
if [[ -f "$ENV_FILE" ]]; then
  ALLOWED="$(grep -E '^ALLOWED_USER_IDS=' "$ENV_FILE" | cut -d= -f2- | tr -d '"' || true)"
fi
if [[ -z "$ALLOWED" ]]; then
  read -r -p "② آیدی عددی تلگرام کاربران مجاز (چندتا با کاما؛ از @userinfobot بگیر): " ALLOWED
fi
[[ -z "$ALLOWED" ]] && die "حداقل یک آیدی عددی لازم است."
c_ok "کاربران مجاز: $ALLOWED"

# ---------- opencode ----------
OPENCODE_BIN="$(command -v opencode || true)"
if [[ -z "$OPENCODE_BIN" ]]; then
  c_warn "opencode نصب نیست؛ در حال نصب…"
  curl -fsSL https://opencode.ai/install | bash || true
  for cand in "$HOME/.opencode/bin/opencode" /usr/local/bin/opencode "$HOME/.local/bin/opencode"; do
    if [[ -x "$cand" ]]; then OPENCODE_BIN="$cand"; break; fi
  done
  [[ -z "$OPENCODE_BIN" ]] && die "نصب opencode ناموفق بود؛ دستی نصب کن و دوباره اجرا کن."
fi
c_ok "opencode: $OPENCODE_BIN ($($OPENCODE_BIN --version 2>/dev/null || true))"

# ---------- آدرس سرور opencode ----------
PORT="${OPENCODE_PORT:-}"
if [[ -f "$ENV_FILE" ]]; then
  PORT="$(grep -E '^OPENCODE_BASE_URL=' "$ENV_FILE" | sed -E 's|.*:([0-9]+)$|\1|' || true)"
fi
if [[ -z "$PORT" ]]; then
  read -r -p "پورت سرور opencode [پیش‌فرض 17890]: " PORT
  PORT="${PORT:-17890}"
fi
BASE_URL="http://127.0.0.1:$PORT"
AGENT="${OPENCODE_AGENT:-build}"

# ---------- باینری پل ----------
BIN="$SRC/$APP"
if [[ ! -x "$BIN" ]]; then
  if ! command -v go >/dev/null 2>&1; then
    c_warn "Go پیدا نشد؛ در حال نصب golang…"
    apt-get update -qq >/dev/null 2>&1 || true
    apt-get install -y golang-go >/dev/null 2>&1 || die "نصب golang ناموفق بود؛ دستی نصب کن:  apt install -y golang-go"
    export PATH="$PATH:/usr/lib/go/bin:/usr/local/go/bin"
  fi
  c_warn "باینری نیست؛ build از سورس…"
  (cd "$SRC" && go build -o "$APP" .) || die "go build ناموفق بود."
fi
c_ok "باینری: $BIN"

# ---------- .env ----------
cat > "$ENV_FILE" <<EOF
TELEGRAM_BOT_TOKEN=$BOT_TOKEN
ALLOWED_USER_IDS=$ALLOWED
OPENCODE_BASE_URL=$BASE_URL
OPENCODE_AGENT=$AGENT
STATE_FILE=$SRC/state.json
EOF
chmod 600 "$ENV_FILE"
c_ok ".env ساخته شد: $ENV_FILE"
c_warn "نکته: مدل و کلید API را بعداً داخل ربات (دکمهٔ ⚙️ تنظیمات) ست می‌کنی."

# ---------- سرویس‌های systemd ----------
cat > "/etc/systemd/system/$SVC_OC" <<EOF
[Unit]
Description=OpenCode headless server (backend for $APP)
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=root
WorkingDirectory=/root
ExecStart=$OPENCODE_BIN serve --port $PORT --hostname 127.0.0.1 --print-logs
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
WorkingDirectory=$SRC
EnvironmentFile=$ENV_FILE
ExecStart=$BIN
Restart=always
RestartSec=5

[Install]
WantedBy=multi-user.target
EOF

systemctl daemon-reload
systemctl enable --now "$SVC_OC" "$SVC_BOT"
c_ok "سرویس‌ها فعال شدند: $SVC_OC و $SVC_BOT"

sleep 2
c_ok "وضعیت:"
systemctl --no-pager is-active "$SVC_OC" "$SVC_BOT" || true

echo "------------------------------------------------------------"
echo "تمام شد!"
echo "  در تلگرام به ربات خودت /start بزن."
echo "  اول دکمهٔ ⚙️ تنظیمات را بزن و «🔑 کلید API» و سپس «🧠 تغییر مدل» را ست کن."
echo "  بعد از ست شدن کلید، ربات ری‌استارت می‌شود و آمادهٔ گفتگوست."
echo "  لاگ‌ها: journalctl -u $SVC_BOT -f"
echo "------------------------------------------------------------"
