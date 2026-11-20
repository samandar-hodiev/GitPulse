# 🚀 GitPulse

**Author:** Samandar Hodiev

GitPulse — GitHub repository'laridagi `push` va `commit` o'zgarishlarini kuzatib,
har bir loyihaning xabarini **o'z Telegram kanaliga** yuboradigan Go backend.

Bitta bot — bir nechta loyiha, bir nechta kanal. uyjoy24 kanaliga faqat uyjoy'ning
push'lari, selfstudy kanaliga faqat selfstudy'niki tushadi.

---

## 📌 Qanday ishlaydi

```
git push
   ↓
GitHub ──► webhook ──┐
                     ├──► GitPulse ──► kanal kaliti ──► o'sha loyihaning kanali
pre-push hook ───────┘       :8080
```

Ikkala yo'l bir vaqtda ishlashi mumkin: GitHub webhook'i (ngrok yoki tunnel orqali)
va loyihaning o'z `pre-push` hook'i (to'g'ridan-to'g'ri `localhost`ga). Bir xil
commit ikki marta kelsa, GitPulse ikkinchisini tashlab yuboradi.

---

## 🔑 Kanal kaliti

Har bir kanal GitPulse'da bitta kalit bilan ro'yxatdan o'tadi:
`gp_` + 32 ta belgi. Kalit ikkita ishni bajaradi:

1. GitHub webhook imzosining siri (HMAC-SHA256)
2. push qaysi kanalga tushishini aniqlaydi

Shuning uchun **yangi loyihani ulash uchun bitta satr yetarli** — o'sha kalit.
Har bir kanalning kaliti alohida: biri sizib ketsa, qolganlari ta'sirlanmaydi.

Kalit hech qachon kanalga yozilmaydi — u bot bilan shaxsiy chatga keladi.

---

## 🆕 Yangi loyihani ulash

### 1. Kanal oching va botni admin qiling

Bot admin bo'lishi bilan kalit shaxsiy chatga keladi.

Agar bot GitPulse ishlamay turgan paytda qo'shilgan bo'lsa — kanalda
`/connect` deb yozing, kalit o'sha zahoti keladi.

### 2. Loyihani kalitga bog'lang

**Lokal hook** (ngrok kerak emas, kompyuter yoniq bo'lsa ishlaydi):

```sh
cd ~/Desktop/<loyiha>
~/Desktop/gitpulse/gitpulse/scripts/install-hook.sh gp_xxxxxxxx
```

Kalit repo ichidagi faylga emas, `.git/config` ga yoziladi — commit'ga tushmaydi.

**GitHub webhook** (Settings → Webhooks → Add webhook):

| Maydon | Qiymat |
|---|---|
| Payload URL | `https://<manzil>/webhook/github/gp_xxxxxxxx` |
| Content type | `application/json` |
| Secret | `gp_xxxxxxxx` |
| Events | Just the push event |

### 3. Tekshiring

Botga `/channels` yozing — kanal va unga bog'langan loyihalar ko'rinadi.

---

## 🤖 Bot buyruqlari

| Buyruq | Nima qiladi |
|---|---|
| `/projects` | Ro'yxatdagi loyihalar |
| `/channels` | Kanallar, kalitlari va bog'langan loyihalar |
| `/bind <loyiha> <raqam>` | Loyihani kanalga qo'lda bog'laydi |
| `/unbind <loyiha>` | Bog'lanishni uzadi |
| `/help` | Yordam |

`/bind` kalit o'rnatilmagan eski loyihalar uchun qulay: push kalitsiz kelsa ham
loyiha nomi bo'yicha to'g'ri kanalga tushadi.

---

## ⚙️ Ishga tushirish

```sh
~/Desktop/gitpulse/gitpulse/scripts/run.sh
```

Terminalda yangi oyna ochib shuni yozish kifoya. Skript `.env` va PostgreSQL
turganini tekshiradi, keyin serverni ko'taradi.

Qo'lda:

```sh
cd ~/Desktop/gitpulse/gitpulse
go run .
```

### `.env`

```
TELEGRAM_BOT_TOKEN=...
TELEGRAM_CHAT_ID=...        # shaxsiy chat: kalitlar va kanalsiz loyihalar shu yerga
GITHUB_WEBHOOK_SECRET=...   # eski, kalitsiz webhook'lar uchun
DATABASE_URL=postgres://.../gitpulse?sslmode=disable
PORT=8080
PUBLIC_BASE_URL=            # ixtiyoriy: ngrok/tunnel manzili, ko'rsatmalarda ishlatiladi
```

### ngrok

GitHub `localhost`ga so'rov yubora olmaydi:

```sh
ngrok http 8080
```

Bepul ngrok manzili har qayta ishga tushganda o'zgaradi — shuning uchun lokal
`pre-push` hook ishonchliroq yo'l. Barqaror manzil kerak bo'lsa: Cloudflare
Tunnel yoki doimiy server.

---

## 🔌 Endpointlar

| Metod | Yo'l | Izoh |
|---|---|---|
| GET | `/health` | Server va baza holati |
| POST | `/webhook/github/<kalit>` | Kalitli webhook — kanal kalitdan aniqlanadi |
| POST | `/webhook/github` | Eski yo'l: umumiy sir, kanal loyiha nomi bo'yicha |
| POST | `/webhook/deploy/<kalit>` | Deploy e'loni — kanalga chiqadi va pin qilinadi |

---

## 🚀 Deploy xabari

Har bir push'dan keyin deploy qilinmaydi — o'n-yigirma push'dan keyin bitta
deploy bo'ladi. Shuning uchun deploy kanalda alohida ko'rinishda chiqadi va
ichida o'sha deployga aynan qaysi o'zgarishlar kirgani sanab o'tiladi.

Deploy tugagandan keyin loyiha papkasidan:

```sh
~/Desktop/gitpulse/gitpulse/scripts/gitpulse-deploy.sh -e prod -u https://uyjoy24.uz
```

| Argument | Nima |
|---|---|
| `-c <sha>` | Deploy qilingan commit (standart: HEAD) |
| `-e <muhit>` | `prod`, `staging` (standart: `prod`) |
| `-u <manzil>` | Sayt manzili |
| `-t <teg>` | Image tegi (standart: qisqa SHA) |
| `-s failed` | Deploy yiqilganini bildiradi |

GitPulse oxirgi deploydan beri kelgan barcha push'larni topadi va har birini
to'liq yozadi: commit, branch, vaqt, muallif va qancha fayl tegilgani.
Ro'yxat quote ichida, **ochiq holatda** turadi — bosish shart emas. Keyin
o'sha commitlar "deploy qilindi" deb belgilanadi va xabar kanalga **pin
qilinadi**. Push xabarlari pin qilinmaydi.

Commitlar ko'p bo'lsa, ro'yxat 12 ta bilan (yoki 3000 belgi bilan)
cheklanadi va oxirida "va yana N ta commit" deb yoziladi — Telegram 4096
belgidan uzun xabarni qabul qilmaydi.

---

## 🗄 Ma'lumotlar bazasi

| Jadval | Nima saqlaydi |
|---|---|
| `projects` | Ko'rilgan loyihalar (nomi, `owner/repo`, GitHub manzili) |
| `channels` | Ulangan kanallar, kalitlari va turi (kanal / guruh) |
| `project_channels` | Qaysi loyiha qaysi kanalga tushadi |
| `pushes` | Har bir commit — deployga nima kirganini aniqlash uchun |
| `project_topics` | Kanalsiz loyihalar uchun shaxsiy chatdagi topiclar |

Sxema ishga tushganda avtomatik yaratiladi.

---

## 🔐 Xavfsizlik

- Har bir so'rov HMAC-SHA256 bilan tekshiriladi (`X-Hub-Signature-256`)
- Har bir kanalning kaliti alohida — umumiy sir emas
- Noma'lum kalit yoki noto'g'ri imzo → `401`
- Kalit kanalga emas, shaxsiy chatga yuboriladi
- `.env` va `.git/config` commit'ga tushmaydi

---

## 📁 Struktura

```
gitpulse/
├── main.go        HTTP server, GitHub webhook, Telegram bot, push kartochkasi
├── channels.go    Kanal registri, kalitlar, yo'naltirish
├── deploy.go      Deploy e'loni, push yozuvlari, pin
├── topics.go      Shaxsiy chatdagi topiclar (kanalsiz loyihalar uchun)
├── format_test.go Xabar formatining testlari
├── scripts/
│   ├── run.sh             ishga tushirish
│   ├── install-hook.sh    pre-push hook'ni istalgan loyihaga o'rnatish
│   ├── gitpulse-notify.sh hook chaqiradigan xabarchi
│   └── gitpulse-deploy.sh deploy tugagach chaqiriladi
├── .env
└── go.mod
```

---

## 📱 Xabar ko'rinishi

Push va deploy birinchi qatordanoq ajralib turadi.

**Push** — 🟡 sariq, nima o'zgargani va qaysi fayllar tegilgani:

```
🟡 COMMIT · PUSH
━━━━━━━━━━━━━━━

📦 uyjoy · 🌿 main
👤 samandar-hodiev · 🔖 0c92b24

📝 fix(console): grafikdagi kalendar — dropdown, kartochkani cho'zmaydi

Kalendar paneli oqimda edi va ochilganda butun Dinamika kartochkasini
pastga cho'zardi. Endi u tugma ostida suzadi.

📊 🟢 1 · 🟡 2 · 🔴 0 — 3 fayl
▸ 🟢 web/src/components/Calendar.tsx
  🟡 web/src/app/page.tsx
  🟡 web/src/lib/routes.ts

🕐 20.11.2026 17:31

🔗 Commit'ni ko'rish
```

Commit tanasi saqlanadi (aynan nima hal qilingani o'sha yerda yozilgan),
lekin 10 qator / 600 belgi bilan cheklanadi, `Co-Authored-By` kabi
trailerlar esa tashlanadi.

**Deploy** — 🟢 yashil, qaysi o'zgarishlar chiqqani vaqti bilan:

```
🟢🚀 DEPLOY
━━━━━━━━━━━━━━━

📦 uyjoy · prod
🌐 https://uyjoy24.uz
🏷 d3afbaa

📚 Shu deployga kirgan 2 ta o'zgarish
🕐 20.11.2026 17:07 → 17:12

▌🔖 5b149a4 · 🌿 main · 🕐 20.11.2026 17:12
▌fix(auth): til almashganda ekran lipillamaydi
▌👤 samandar-hodiev  ·  🟢 0 · 🟡 1 · 🔴 0 — 1 fayl
▌
▌🔖 6ca982c · 🌿 main · 🕐 20.11.2026 17:07
▌feat(console): bosh panel grafigi — tepada, 24 soat va kalendar
▌👤 samandar-hodiev  ·  🟢 8 · 🟡 19 · 🔴 0 — 27 fayl

🕐 Deploy: 20.11.2026 19:41

🔗 Deploy qilingan versiya
```

Bitta push'da bir nechta commit bo'lsa, hammasi bitta xabarda ko'rinadi —
kanal o'nta kartochka bilan to'lib ketmaydi.

---

## ✅ Holat

| | |
|---|---|
| GitHub webhook | ✅ |
| Telegram xabar | ✅ |
| Ko'p loyiha | ✅ |
| Kanal bo'yicha yo'naltirish | ✅ |
| Kalit bilan ulanish | ✅ |
| Webhook xavfsizligi (HMAC) | ✅ |
| Dublikatni bostirish | ✅ |
| PostgreSQL | ✅ |
| Bot buyruqlari | ✅ |
| Lokal pre-push hook | ✅ |
| Rate limit (429) ishlovi | ✅ |
| Deploy e'loni + pin | ✅ |
| Xabar formati testlari | ✅ |
| Doimiy server / tunnel | ⬜ |
| Pull Request, Issue, CI xabarlari | ⬜ |
