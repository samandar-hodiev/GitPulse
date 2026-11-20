#!/bin/sh
# GitPulse pre-push hook'ini istalgan loyihaga o'rnatadi.
#
# Foydalanish (loyiha papkasidan turib):
#
#   /yo'l/gitpulse/scripts/install-hook.sh gp_xxxxxxxx
#
# yoki boshqa papkadagi loyihaga:
#
#   /yo'l/gitpulse/scripts/install-hook.sh gp_xxxxxxxx ~/Desktop/Sat-Platform
#
# Kalit kanalni ochganda botdan keladi (/channels buyrug'i ham ko'rsatadi).
# Kalitsiz ishga tushirilsa, hook o'rnatiladi va eski umumiy sir ishlatiladi.

set -eu

KEY="${1:-}"
REPO="${2:-$(pwd)}"

SCRIPT_DIR=$(cd "$(dirname "$0")" && pwd)
NOTIFY="$SCRIPT_DIR/gitpulse-notify.sh"

if [ ! -x "$NOTIFY" ]; then
  echo "install-hook: $NOTIFY topilmadi yoki bajariladigan emas" >&2
  exit 1
fi

cd "$REPO"

if ! git rev-parse --git-dir >/dev/null 2>&1; then
  echo "install-hook: $REPO git repo emas" >&2
  exit 1
fi

GIT_DIR=$(git rev-parse --git-dir)
HOOK="$GIT_DIR/hooks/pre-push"

if [ -n "$KEY" ]; then
  git config gitpulse.key "$KEY"
  echo "🔑 Kalit saqlandi: $GIT_DIR/config (gitpulse.key)"
fi

if [ -e "$HOOK" ] && ! grep -q 'gitpulse-notify' "$HOOK" 2>/dev/null; then
  cp "$HOOK" "$HOOK.backup"
  echo "ℹ️  Eski pre-push saqlandi: $HOOK.backup"
fi

cat > "$HOOK" <<EOF
#!/bin/sh
# pre-push: commitlar remote'ga haqiqatan tushgach GitPulse'ga xabar beradi.
#
# O'rnatilgan: $SCRIPT_DIR/install-hook.sh
#
# Git'da post-push hook yo'q, shuning uchun darhol yubormay, fonda kuzatuvchi
# ishga tushadi: u remote o'sha SHA'ni ko'rsatguncha so'raydi va shundan keyin
# xabar beradi. Rad etilgan push hech qanday Telegram xabari qoldirmaydi.
#
# Hook har doim 0 qaytaradi: xabar muammosi push'ni to'xtatmasligi kerak.

set -u

NOTIFY="$NOTIFY"
[ -x "\$NOTIFY" ] || exit 0

REMOTE_NAME="\${1:-origin}"
SEEN=""

while read -r _local_ref local_sha remote_ref _remote_sha; do
  [ -n "\$local_sha" ] || continue
  [ -n "\$remote_ref" ] || continue
  case "\$local_sha" in
    *[!0]*) ;;
    *) continue ;;
  esac
  case " \$SEEN " in
    *" \$local_sha "*) continue ;;
  esac
  SEEN="\$SEEN \$local_sha"

  (
    i=0
    while [ "\$i" -lt 30 ]; do
      sleep 1
      actual=\$(git ls-remote "\$REMOTE_NAME" "\$remote_ref" 2>/dev/null | awk '{print \$1}')
      if [ "\$actual" = "\$local_sha" ]; then
        "\$NOTIFY" "\$local_sha" "\${remote_ref#refs/heads/}" || true
        exit 0
      fi
      i=\$((i + 1))
    done
  ) >/dev/null 2>&1 &
done

exit 0
EOF

chmod +x "$HOOK"

echo "✅ pre-push hook o'rnatildi: $HOOK"
echo "   Loyiha: $(git config --get remote.origin.url 2>/dev/null || echo '(remote yo‘q)')"

if [ -z "$KEY" ] && [ -z "$(git config --get gitpulse.key || true)" ]; then
  echo "⚠️  Kalit berilmadi — xabar eski umumiy sir bilan yuboriladi."
  echo "   Kanalga bog'lash uchun: git config gitpulse.key gp_..."
fi
