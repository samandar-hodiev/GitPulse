#!/bin/sh
# GitPulse'ni lokalda ishga tushiradi.
#
# Terminalda yangi oyna ochib shuni yozish kifoya:
#
#   ~/Desktop/gitpulse/gitpulse/scripts/run.sh
#
# Qaysi papkadan chaqirilishidan qat'i nazar ishlaydi.

set -eu

ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$ROOT"

if [ ! -f .env ]; then
  echo "❌ .env topilmadi: $ROOT/.env" >&2
  exit 1
fi

# Postgres turmasa GitPulse ko'tarilmaydi — sababini oldindan aytamiz.
DB=$(grep '^DATABASE_URL=' .env | cut -d= -f2- || true)

if [ -n "$DB" ] && command -v pg_isready >/dev/null 2>&1; then
  if ! pg_isready -q -d "$DB" 2>/dev/null; then
    echo "⚠️  PostgreSQL javob bermayapti: $(echo "$DB" | sed 's#//[^@]*@#//***@#')" >&2
    echo "   Ishga tushiring, keyin qaytadan urinib ko'ring." >&2
    exit 1
  fi
fi

echo "⚡ GitPulse ishga tushmoqda — $ROOT"
exec go run .
