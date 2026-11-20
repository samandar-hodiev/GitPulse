#!/bin/sh
# GitPulse'ga (lokal GitHub → Telegram xabarchisi) remote'ga endigina tushgan
# commit haqida xabar beradi.
#
# Nega kerak: GitHub webhook'i GitPulse'ga ngrok orqali yetadi, bepul ngrok
# manzili esa har qayta ishga tushganda o'zgaradi. Bu skript — lokal yo'l:
# GitHub yuboradigan payload'ni o'zi yig'adi, o'sha kalit bilan imzolaydi va
# to'g'ridan-to'g'ri GitPulse'ga POST qiladi. Ikkala yo'l bir vaqtda ishlashi
# mumkin — GitPulse dublikatni o'zi tashlaydi.
#
# Kanal kaliti loyihaning o'z git konfiguratsiyasidan olinadi:
#
#   git config gitpulse.key gp_xxxxxxxx
#
# Kalit repo ichidagi faylda emas, `.git/config` da turadi: commit'ga tushmaydi
# va har bir loyiha faqat o'z kanalining kalitiga ega bo'ladi.
#
# Foydalanish: gitpulse-notify.sh <commit-sha> [branch]

set -eu

SHA="${1:?usage: gitpulse-notify.sh <commit-sha> [branch]}"
BRANCH="${2:-$(git rev-parse --abbrev-ref HEAD)}"

GITPULSE_HOST="${GITPULSE_HOST:-http://localhost:8080}"
GITPULSE_HEALTH="${GITPULSE_HEALTH:-$GITPULSE_HOST/health}"

KEY="${GITPULSE_KEY:-$(git config --get gitpulse.key || true)}"

if [ -n "$KEY" ]; then
  # Yangi yo'l: kalit ham imzo siri, ham kanal manzili.
  GITPULSE_URL="$GITPULSE_HOST/webhook/github/$KEY"
  SECRET="$KEY"
else
  # Eski yo'l: umumiy sir. Kanal loyiha nomi bo'yicha topiladi.
  GITPULSE_DIR="${GITPULSE_DIR:-$HOME/Desktop/gitpulse/gitpulse}"
  ENV_FILE="$GITPULSE_DIR/.env"

  if [ ! -f "$ENV_FILE" ]; then
    echo "gitpulse-notify: kalit yo'q va $ENV_FILE topilmadi, xabar yuborilmadi" >&2
    echo "  tuzatish: git config gitpulse.key gp_..." >&2
    exit 0
  fi

  SECRET=$(grep '^GITHUB_WEBHOOK_SECRET=' "$ENV_FILE" | cut -d= -f2-)
  GITPULSE_URL="$GITPULSE_HOST/webhook/github"

  if [ -z "$SECRET" ]; then
    echo "gitpulse-notify: sir bo'sh, xabar yuborilmadi" >&2
    exit 0
  fi
fi

if ! curl -fsS -m 3 -o /dev/null "$GITPULSE_HEALTH" 2>/dev/null; then
  echo "gitpulse-notify: GitPulse javob bermayapti, xabar yuborilmadi" >&2
  exit 0
fi

REPO_URL=$(git config --get remote.origin.url | sed -e 's#\.git$##' -e 's#^git@github.com:#https://github.com/#')
REPO_NAME=$(basename "$REPO_URL")
REPO_OWNER=$(basename "$(dirname "$REPO_URL")")

PAYLOAD=$(
  SHA="$SHA" REPO_URL="$REPO_URL" REPO_NAME="$REPO_NAME" \
  REPO_FULL="$REPO_OWNER/$REPO_NAME" BRANCH="$BRANCH" \
  python3 -I - <<'PY'
import json, os, subprocess

sha = os.environ["SHA"]

def git(*args):
    return subprocess.run(["git", *args], capture_output=True, text=True).stdout.strip()

added, modified, removed = [], [], []
for line in git("show", "--name-status", "--pretty=", sha).splitlines():
    parts = line.split("\t")
    if len(parts) < 2:
        continue
    status, path = parts[0][:1], parts[-1]
    {"A": added, "M": modified, "D": removed}.get(status, modified).append(path)

commit = {
    "id": sha,
    "message": git("log", "-1", "--pretty=%B", sha),
    "timestamp": git("log", "-1", "--date=iso-strict", "--pretty=%ad", sha),
    "url": f"{os.environ['REPO_URL']}/commit/{sha}",
    "author": {
        "name": git("log", "-1", "--pretty=%an", sha),
        "email": git("log", "-1", "--pretty=%ae", sha),
    },
    "added": added,
    "modified": modified,
    "removed": removed,
}

payload = {
    "ref": f"refs/heads/{os.environ['BRANCH']}",
    "repository": {
        "name": os.environ["REPO_NAME"],
        "full_name": os.environ["REPO_FULL"],
        "html_url": os.environ["REPO_URL"],
    },
    "head_commit": commit,
    "commits": [commit],
}
print(json.dumps(payload))
PY
)

SIG="sha256=$(printf '%s' "$PAYLOAD" | openssl dgst -sha256 -hmac "$SECRET" -hex | awk '{print $NF}')"

RESPONSE=$(printf '%s' "$PAYLOAD" | curl -s -m 15 -w '\n%{http_code}' \
  -X POST "$GITPULSE_URL" \
  -H 'Content-Type: application/json' \
  -H 'X-GitHub-Event: push' \
  -H "X-Hub-Signature-256: $SIG" \
  --data-binary @-)
CODE=$(printf '%s' "$RESPONSE" | tail -n1)
BODY=$(printf '%s' "$RESPONSE" | sed '$d')
SHORT=$(printf '%s' "$SHA" | cut -c1-7)

case "$CODE:$BODY" in
  200:Duplicate*) echo "gitpulse-notify: $SHORT allaqachon Telegramga yuborilgan" ;;
  200:*)          echo "gitpulse-notify: Telegramga yuborildi ($SHORT)" ;;
  *)              echo "gitpulse-notify: GitPulse $CODE qaytardi: $BODY" >&2 ;;
esac

# pre-push hook buni fonda ishga tushiradi, shuning uchun lokal iz qoldiramiz.
LOG_DIR="$(git rev-parse --git-dir)/gitpulse"
mkdir -p "$LOG_DIR" && printf '%s %s %s %s\n' "$(date '+%F %T')" "$SHORT" "$CODE" "$BODY" >> "$LOG_DIR/notify.log"
