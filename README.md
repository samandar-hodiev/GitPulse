# 🚀 GitPulse

**Author:** Samandar Hodiev

GitPulse — GitHub repository'laridagi `push` va `commit` o'zgarishlarini kuzatib, ularni Telegram orqali real-time notification ko'rinishida yuboradigan Go backend application.

GitPulse bir nechta GitHub projectlarini bitta Telegram bot orqali kuzatish uchun mo'ljallangan.

---

## 📌 GitPulse nima?

GitPulse — GitHub va Telegram o'rtasidagi notification bridge.

Developer GitHub repository'ga `git push` qilganda:

Developer
    ↓
git commit
    ↓
git push
    ↓
GitHub
    ↓
GitHub Webhook
    ↓
GitPulse
    ↓
Telegram Bot
    ↓
Developer

GitPulse GitHub webhook orqali kelgan ma'lumotlarni qabul qiladi, qayta ishlaydi va Telegram orqali notification yuboradi.

---

## 🎯 Asosiy vazifasi

GitPulse'ning asosiy vazifasi — GitHub'dagi yangi push va commit'larni avtomatik kuzatish va developerga Telegram orqali xabar berish.

Masalan:

🚀 PUSH

📦 Project
Rent-House

👤 Author
samandar-hodiev

🌿 Branch
main

📝 Commit
feat: add apartment recommendations

🔖 Commit ID
a0aa5b1

📊 Changes
• 3 files changed
• 🟢 1 added
• 🟡 2 modified
• 🔴 0 removed

🕐 Time
17.08.2026 15:38:32

🔗 View commit on GitHub

---

# ✨ Asosiy imkoniyatlar

## 1. GitHub Webhook

GitPulse GitHub repository'dan webhook orqali eventlarni qabul qiladi.

Hozirgi asosiy event:

- `push`

GitHub webhook request yuborganda GitPulse uni JSON orqali parse qiladi.

---

## 2. Real-time Telegram Notification

GitHub'ga push qilinganidan keyin GitPulse Telegram Bot API orqali notification yuboradi.

Developer GitHub'ni doimiy ravishda tekshirib o'tirmaydi.

Notification avtomatik keladi.

---

## 3. Real Commit Data

GitPulse commit ma'lumotlarini GitHub webhook payload'idan avtomatik oladi.

Quyidagi ma'lumotlar ishlatiladi:

- Project name
- Author
- Branch
- Commit message
- Commit ID
- Commit URL
- Commit time
- Added files
- Modified files
- Removed files
- Total changed files

Ma'lumotlar hardcode qilinmaydi.

---

## 4. Multi-Project Support

GitPulse faqat bitta project uchun emas.

Bitta GitPulse instance orqali bir nechta GitHub repository'larini kuzatish mumkin.

Masalan:

- Rent-House
- Go-Backend
- Portfolio
- Telegram-Bot
- E-Commerce
- boshqa GitHub repository'lar

Har bir repository GitPulse'ga o'z webhook'i orqali ulanadi.

Telegram notification ichida project nomi ko'rsatiladi.

Masalan:

📦 Project
Rent-House

yoki:

📦 Project
Go-Backend

---

# 💡 GitPulse qanday ishlaydi?

GitPulse quyidagi pipeline orqali ishlaydi:

GitHub Repository
        ↓
GitHub Webhook
        ↓
ngrok / Production Server
        ↓
Go HTTP Server
        ↓
JSON Parser
        ↓
GitHub Event Handler
        ↓
Telegram Bot API
        ↓
Telegram Notification

---

# 🧠 Nima uchun GitPulse kerak?

Oddiy holatda developer GitHub activity'ni tekshirish uchun:

GitHub → Repository → Commits

bo'limiga kirishi kerak.

GitPulse esa bu jarayonni avtomatlashtiradi:

git push
    ↓
Telegram notification

Shuning uchun developer:

- GitHub'ni doimiy tekshirib o'tirmaydi
- Qaysi project o'zgarganini tez biladi
- Kim push qilganini ko'radi
- Qaysi branch o'zgarganini ko'radi
- Commit message'ni ko'radi
- Qancha file o'zgarganini ko'radi
- Commit'ga to'g'ridan-to'g'ri o'tishi mumkin

---

# 🏗️ Architecture

Hozirgi architecture:

Developer
    │
    │ git push
    ▼
GitHub
    │
    │ Webhook
    ▼
ngrok
    │
    │ HTTP POST
    ▼
GitPulse
    │
    │ Telegram Bot API
    ▼
Telegram

Production'da ngrok o'rniga public server ishlatiladi.

---

# 🛠️ Tech Stack

## Backend

- Go
- net/http
- encoding/json
- net/url
- html
- time
- logging

## External Services

- GitHub Webhooks
- Telegram Bot API
- ngrok

## Configuration

- Environment Variables
- `.env`

---

# 📂 Project Structure

Hozirgi project structure:

GitPulse/
│
├── main.go
├── go.mod
├── go.sum
├── .env
├── .gitignore
└── README.md

Project rivojlangani sari modular architecture'ga o'tkaziladi:

GitPulse/
│
├── cmd/
│   └── server/
│       └── main.go
│
├── internal/
│   ├── github/
│   ├── telegram/
│   ├── webhook/
│   ├── notification/
│   └── config/
│
├── .env
├── .gitignore
├── go.mod
├── go.sum
└── README.md

---

# ⚙️ Environment Variables

GitPulse sensitive ma'lumotlarni environment variables orqali oladi.

`.env`:

TELEGRAM_BOT_TOKEN=your_bot_token
TELEGRAM_CHAT_ID=your_chat_id

`.env` fayli GitHub repository'ga push qilinmasligi kerak.

`.gitignore`:

.env

---

# 🚀 Local Development

## 1. Repository'ni clone qilish

git clone https://github.com/USERNAME/GitPulse.git

cd GitPulse

## 2. Dependencies

go mod download

## 3. `.env` yaratish

TELEGRAM_BOT_TOKEN=your_bot_token
TELEGRAM_CHAT_ID=your_chat_id

## 4. Serverni ishga tushirish

go run .

Server:

http://localhost:8080

manzilida ishlaydi.

---

# 🔌 API Endpoints

## Health Check

GET /health

Response:

GitPulse is running

---

## GitHub Webhook

POST /webhook/github

GitHub push eventlari shu endpoint orqali qabul qilinadi.

---

# 🌐 ngrok bilan ishlash

Development vaqtida GitHub localhost'ga request yubora olmaydi.

Shuning uchun ngrok ishlatiladi:

ngrok http 8080

Ngrok public URL beradi:

https://example.ngrok-free.dev

GitHub webhook URL:

https://example.ngrok-free.dev/webhook/github

---

# 🔗 GitHub Webhook Setup

GitHub repository:

Settings
    ↓
Webhooks
    ↓
Add webhook

Payload URL:

https://YOUR-NGROK-URL/webhook/github

Content type:

application/json

Events:

Just the push event

Active:

Enabled

---

# 📱 Telegram Notification

GitHub'da push bo'lganda Telegram bot notification yuboradi:

🚀 PUSH

📦 Project
Rent-House

👤 Author
samandar-hodiev

🌿 Branch
main

📝 Commit
feat: add apartment recommendations

🔖 Commit ID
a0aa5b1

📊 Changes
• 3 files changed
• 🟢 1 added
• 🟡 2 modified
• 🔴 0 removed

🕐 Time
17.08.2026 15:38:32

🔗 View commit on GitHub

Telegram link preview o'chirilgan, shuning uchun notification faqat kerakli ma'lumotlarni ko'rsatadi.

---

# 🔐 Security

Webhook security GitPulse'ning muhim qismlaridan biri.

Rejalashtirilgan security:

- GitHub Webhook Secret
- `X-Hub-Signature-256` verification
- Fake webhook request'larni bloklash
- Request validation
- Environment variables orqali secretlarni saqlash
- Telegram Bot Token'ni himoyalash
- Database credentials'ni himoyalash

Sensitive ma'lumotlar Git repository'ga commit qilinmasligi kerak.

---

# 🎯 Roadmap

## Phase 1 — Core

- [x] Go HTTP server
- [x] GitHub Webhook
- [x] GitHub push event
- [x] JSON payload parsing
- [x] Real commit data
- [x] Telegram Bot API
- [x] Telegram notification
- [x] Project name detection
- [x] Branch detection
- [x] Commit information
- [x] File change statistics
- [x] GitHub commit link
- [x] Disable Telegram link preview
- [x] Multi-project architecture

## Phase 2 — Security

- [ ] GitHub Webhook Secret
- [ ] `X-Hub-Signature-256` verification
- [ ] Invalid webhook rejection
- [ ] Request validation
- [ ] Better error handling
- [ ] Logging

## Phase 3 — Telegram Bot

- [ ] `/start`
- [ ] `/help`
- [ ] `/projects`
- [ ] Project list
- [ ] Project filtering
- [ ] Project enable/disable
- [ ] Notification settings

## Phase 4 — Better Notifications

- [ ] Better Telegram UI
- [ ] Inline buttons
- [ ] Commit buttons
- [ ] Project-specific formatting
- [ ] Push summary
- [ ] Multiple commits in one push
- [ ] Better change statistics
- [ ] File change list

## Phase 5 — Database

- [ ] PostgreSQL
- [ ] Project model
- [ ] User model
- [ ] Telegram chat model
- [ ] Webhook configuration
- [ ] Project settings
- [ ] Notification history

## Phase 6 — Production

- [ ] Production server
- [ ] HTTPS
- [ ] Domain
- [ ] Docker
- [ ] CI/CD
- [ ] Environment management
- [ ] Monitoring
- [ ] Logging
- [ ] Automatic deployment

---

# 🔮 Future Features

GitPulse kelajakda oddiy GitHub → Telegram notification botdan to'liq developer activity monitoring tool'ga aylanishi mumkin.

Kelajakdagi imkoniyatlar:

- Commit statistics
- Daily development summary
- Weekly development summary
- Project activity statistics
- Multiple developers
- Multiple Telegram users
- Pull Request notifications
- Issue notifications
- Branch notifications
- Deployment notifications
- CI/CD status notifications
- GitHub Actions notifications
- Failed build alerts
- Project analytics

---

# 💼 Real-world Use Case

Developer bir nechta project bilan ishlaydi:

- Rent-House
- E-Commerce
- Go API
- Telegram Bot
- Portfolio

Developer har safar:

git push

qilganda GitPulse Telegramga avtomatik notification yuboradi.

Shunday qilib developer Telegram orqali barcha projectlaridagi activity'ni kuzatishi mumkin.

---


# 📌 Project Status

🚧 GitPulse hozir development bosqichida.

Current status:

GitHub Webhook        ✅
Telegram Notification ✅
Real Commit Data      ✅
Multi-Project         ✅
Link Preview Removal  ✅
Webhook Security      🚧
Telegram Commands     🚧
Database              ⬜
Production Deploy     ⬜