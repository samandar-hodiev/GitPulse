#!/bin/sh
# GitPulse'ga deploy bo'lgani haqida xabar beradi.
#
# Push xabaridan farqi: deploy kanalda alohida ko'rinishda chiqadi, ichida
# o'sha deployga kirgan barcha commitlar vaqti bilan sanab o'tiladi va xabar
# kanalga pin qilinadi.
#
# Loyiha papkasidan turib chaqiriladi — deploy tugagandan keyin:
#
#   ~/Desktop/gitpulse/gitpulse/scripts/gitpulse-deploy.sh
#
# Ixtiyoriy argumentlar:
#
#   -c <sha>      deploy qilingan commit (standart: HEAD)
#   -e <muhit>    prod / staging (standart: prod)
#   -u <manzil>   https://uyjoy24.uz
#   -t <teg>      image tegi (standart: qisqa SHA)
#   -s failed     deploy yiqilganini bildiradi
#
# Kalit loyihaning git konfiguratsiyasidan olinadi (git config gitpulse.key).

set -eu

COMMIT=""
ENVIRONMENT="prod"
SITE_URL=""
IMAGE_TAG=""
STATUS="success"

while getopts "c:e:u:t:s:" opt; do
  case "$opt" in
    c) COMMIT="$OPTARG" ;;
    e) ENVIRONMENT="$OPTARG" ;;
    u) SITE_URL="$OPTARG" ;;
    t) IMAGE_TAG="$OPTARG" ;;
    s) STATUS="$OPTARG" ;;
    *) echo "foydalanish: gitpulse-deploy.sh [-c sha] [-e muhit] [-u url] [-t teg] [-s failed]" >&2; exit 2 ;;
  esac
done

GITPULSE_HOST="${GITPULSE_HOST:-http://localhost:8080}"
GITPULSE_HEALTH="${GITPULSE_HEALTH:-$GITPULSE_HOST/health}"

KEY="${GITPULSE_KEY:-$(git config --get gitpulse.key || true)}"

if [ -z "$KEY" ]; then
  echo "gitpulse-deploy: kalit yo'q. Tuzatish: git config gitpulse.key gp_..." >&2
  exit 0
fi

if ! curl -fsS -m 3 -o /dev/null "$GITPULSE_HEALTH" 2>/dev/null; then
  echo "gitpulse-deploy: GitPulse javob bermayapti, xabar yuborilmadi" >&2
  exit 0
fi

[ -n "$COMMIT" ] || COMMIT=$(git rev-parse HEAD)
[ -n "$IMAGE_TAG" ] || IMAGE_TAG=$(printf '%s' "$COMMIT" | cut -c1-7)

REPO_URL=$(git config --get remote.origin.url | sed -e 's#\.git$##' -e 's#^git@github.com:#https://github.com/#')
REPO_NAME=$(basename "$REPO_URL")
REPO_OWNER=$(basename "$(dirname "$REPO_URL")")

PAYLOAD=$(
  COMMIT="$COMMIT" ENVIRONMENT="$ENVIRONMENT" SITE_URL="$SITE_URL" \
  IMAGE_TAG="$IMAGE_TAG" STATUS="$STATUS" REPO_URL="$REPO_URL" \
  REPO_NAME="$REPO_NAME" REPO_FULL="$REPO_OWNER/$REPO_NAME" \
  python3 -I - <<'PY'
import json, os
print(json.dumps({
    "project": os.environ["REPO_NAME"],
    "full_name": os.environ["REPO_FULL"],
    "environment": os.environ["ENVIRONMENT"],
    "url": os.environ["SITE_URL"],
    "commit": os.environ["COMMIT"],
    "image_tag": os.environ["IMAGE_TAG"],
    "repo_url": os.environ["REPO_URL"],
    "status": os.environ["STATUS"],
}))
PY
)

SIG="sha256=$(printf '%s' "$PAYLOAD" | openssl dgst -sha256 -hmac "$KEY" -hex | awk '{print $NF}')"

RESPONSE=$(printf '%s' "$PAYLOAD" | curl -s -m 20 -w '\n%{http_code}' \
  -X POST "$GITPULSE_HOST/webhook/deploy/$KEY" \
  -H 'Content-Type: application/json' \
  -H "X-Hub-Signature-256: $SIG" \
  --data-binary @-)

CODE=$(printf '%s' "$RESPONSE" | tail -n1)
BODY=$(printf '%s' "$RESPONSE" | sed '$d')
SHORT=$(printf '%s' "$COMMIT" | cut -c1-7)

case "$CODE:$BODY" in
  200:Duplicate*) echo "gitpulse-deploy: $SHORT allaqachon e'lon qilingan" ;;
  200:*)          echo "gitpulse-deploy: deploy kanalga yuborildi va pin qilindi ($SHORT)" ;;
  *)              echo "gitpulse-deploy: GitPulse $CODE qaytardi: $BODY" >&2 ;;
esac
