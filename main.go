package main

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "github.com/jackc/pgx/v5/stdlib"
	"github.com/joho/godotenv"
)

// ============================================================
// GITHUB TYPES
// ============================================================

type GitHubPayload struct {
	Ref string `json:"ref"`

	Repository struct {
		Name     string `json:"name"`
		FullName string `json:"full_name"`
		HTMLURL  string `json:"html_url"`
	} `json:"repository"`

	HeadCommit *Commit `json:"head_commit"`

	// Commits — push ichidagi barcha commitlar (GitHub ko'pi bilan 20 tasini
	// yuboradi). Faqat head_commit'ni olish bitta push'da bir nechta commit
	// bo'lganda qolganlarini ko'rinmas qilardi.
	Commits []Commit `json:"commits"`
}

type Commit struct {
	ID        string `json:"id"`
	Message   string `json:"message"`
	Timestamp string `json:"timestamp"`
	URL       string `json:"url"`

	Author struct {
		Name  string `json:"name"`
		Email string `json:"email"`
	} `json:"author"`

	Added    []string `json:"added"`
	Modified []string `json:"modified"`
	Removed  []string `json:"removed"`
}

// ============================================================
// TELEGRAM TYPES
// ============================================================

type TelegramUpdate struct {
	UpdateID int64 `json:"update_id"`

	Message     *TelegramMessage `json:"message"`
	ChannelPost *TelegramMessage `json:"channel_post"`

	// MyChatMember bot biror chatga qo'shilgani yoki admin qilinganida
	// keladi. Kanalni avtomatik ro'yxatga olish shunga tayanadi.
	MyChatMember *ChatMemberUpdated `json:"my_chat_member"`
}

type TelegramMessage struct {
	MessageID int64        `json:"message_id"`
	Chat      TelegramChat `json:"chat"`
	Text      string       `json:"text"`
}

type TelegramChat struct {
	ID    int64  `json:"id"`
	Type  string `json:"type"`
	Title string `json:"title"`
}

type TelegramUser struct {
	ID int64 `json:"id"`
}

type ChatMemberUpdated struct {
	Chat TelegramChat `json:"chat"`
	From TelegramUser `json:"from"`

	NewChatMember struct {
		Status string `json:"status"`
	} `json:"new_chat_member"`
}

// ============================================================
// DATABASE
// ============================================================

var db *sql.DB

// ============================================================
// CONFIG
// ============================================================

type Config struct {
	TelegramBotToken    string
	TelegramChatID      string
	GitHubWebhookSecret string
	DatabaseURL         string
	Port                string

	// PublicBaseURL — GitHub ko'radigan manzil (ngrok yoki tunnel URL'i).
	// Faqat ko'rsatma matnlarida ishlatiladi.
	PublicBaseURL string
}

var config Config

// ScriptsDir — install-hook.sh turgan papka. Ko'rsatma matnida to'liq yo'l
// ko'rinsin uchun ishchi papkadan olinadi.
func (c Config) ScriptsDir() string {

	wd, err := os.Getwd()

	if err != nil {
		return "scripts"
	}

	return wd + "/scripts"
}

// ============================================================
// MAIN
// ============================================================

func main() {

	// --------------------------------------------------------
	// LOAD ENV
	// --------------------------------------------------------

	if err := godotenv.Load(); err != nil {
		log.Println("⚠️ Warning: .env file not found")
	}

	loadConfig()

	// --------------------------------------------------------
	// DATABASE
	// --------------------------------------------------------

	if err := initDatabase(); err != nil {
		log.Fatal("❌ Database initialization failed:", err)
	}

	defer db.Close()

	if err := initDatabaseSchema(); err != nil {
		log.Fatal("❌ Database schema initialization failed:", err)
	}

	// --------------------------------------------------------
	// TELEGRAM
	// --------------------------------------------------------

	if err := deleteTelegramWebhook(); err != nil {
		log.Println(
			"⚠️ Telegram webhook cleanup warning:",
			err,
		)
	}

	// --------------------------------------------------------
	// HTTP ROUTES
	// --------------------------------------------------------

	mux := http.NewServeMux()

	mux.HandleFunc("/health", health)

	// Eski yo'l: umumiy sir bilan imzolangan, kanal loyiha nomi bo'yicha
	// topiladi. Yangi yo'l: /webhook/github/<kalit> — imzo ham, kanal ham
	// o'sha kalitdan keladi.
	mux.HandleFunc("/webhook/github", githubWebhook)
	mux.HandleFunc("/webhook/github/", githubWebhook)

	// Deploy — push'dan alohida hodisa: o'nlab push'dan keyin bitta
	// deploy bo'ladi va kanalda boshqacha ko'rinadi.
	mux.HandleFunc("/webhook/deploy/", deployWebhook)

	// --------------------------------------------------------
	// TELEGRAM BOT
	// --------------------------------------------------------

	go startTelegramBot()

	// Turi noma'lum kanallarni Telegram'dan so'rab to'ldiramiz.
	go refreshChannelTypes()

	// --------------------------------------------------------
	// SERVER
	// --------------------------------------------------------

	fmt.Printf(
		"⚡ GitPulse server started on :%s\n",
		config.Port,
	)

	// Timeout'lar: timeout'siz server sekin yoki osilib qolgan ulanishda
	// goroutine va fayl deskriptorlarini ushlab turadi.
	server := &http.Server{
		Addr:              ":" + config.Port,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	log.Fatal(server.ListenAndServe())
}

// ============================================================
// CONFIG
// ============================================================

func loadConfig() {

	config = Config{
		TelegramBotToken:    os.Getenv("TELEGRAM_BOT_TOKEN"),
		TelegramChatID:      os.Getenv("TELEGRAM_CHAT_ID"),
		GitHubWebhookSecret: os.Getenv("GITHUB_WEBHOOK_SECRET"),
		DatabaseURL:         os.Getenv("DATABASE_URL"),
		Port:                os.Getenv("PORT"),
		PublicBaseURL:       os.Getenv("PUBLIC_BASE_URL"),
	}

	if config.TelegramBotToken == "" {
		log.Fatal("❌ TELEGRAM_BOT_TOKEN is missing")
	}

	if config.TelegramChatID == "" {
		log.Fatal("❌ TELEGRAM_CHAT_ID is missing")
	}

	if config.GitHubWebhookSecret == "" {
		log.Fatal("❌ GITHUB_WEBHOOK_SECRET is missing")
	}

	if config.DatabaseURL == "" {
		log.Fatal("❌ DATABASE_URL is missing")
	}

	if config.Port == "" {
		config.Port = "8080"
	}

	if config.PublicBaseURL == "" {
		config.PublicBaseURL = "http://localhost:" + config.Port
	}
}

// ============================================================
// DATABASE INIT
// ============================================================

func initDatabase() error {

	var err error

	db, err = sql.Open(
		"pgx",
		config.DatabaseURL,
	)

	if err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(
		context.Background(),
		10*time.Second,
	)

	defer cancel()

	if err := db.PingContext(ctx); err != nil {
		return err
	}

	log.Println("🗄 PostgreSQL connected")

	return nil
}

// ============================================================
// DATABASE SCHEMA
// ============================================================

func initDatabaseSchema() error {

	// --------------------------------------------------------
	// CREATE TABLE
	// --------------------------------------------------------

	query := `
	CREATE TABLE IF NOT EXISTS projects (
		name TEXT PRIMARY KEY,
		github_url TEXT NOT NULL DEFAULT '',
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	`

	if _, err := db.Exec(query); err != nil {
		return err
	}

	// --------------------------------------------------------
	// OLD DATABASE MIGRATION
	// --------------------------------------------------------

	migration := `
	ALTER TABLE projects
	ADD COLUMN IF NOT EXISTS github_url TEXT NOT NULL DEFAULT '';

	ALTER TABLE projects
	ADD COLUMN IF NOT EXISTS full_name TEXT NOT NULL DEFAULT '';
	`

	if _, err := db.Exec(migration); err != nil {
		return err
	}

	if err := initTopicsSchema(); err != nil {
		return err
	}

	if err := initChannelsSchema(); err != nil {
		return err
	}

	if err := initDeploySchema(); err != nil {
		return err
	}

	log.Println("🗄 Database schema ready")

	return nil
}

// ============================================================
// HEALTH
// ============================================================

func health(
	w http.ResponseWriter,
	r *http.Request,
) {

	if r.Method != http.MethodGet {

		http.Error(
			w,
			"Method not allowed",
			http.StatusMethodNotAllowed,
		)

		return
	}

	ctx, cancel := context.WithTimeout(
		r.Context(),
		2*time.Second,
	)

	defer cancel()

	if err := db.PingContext(ctx); err != nil {

		http.Error(
			w,
			"Database unavailable",
			http.StatusServiceUnavailable,
		)

		return
	}

	w.WriteHeader(http.StatusOK)

	_, _ = w.Write(
		[]byte("GitPulse is running"),
	)
}

// ============================================================
// GITHUB WEBHOOK
// ============================================================

// ============================================================
// DUPLICATE SUPPRESSION
// ============================================================

// The same push can arrive twice. GitHub delivers it through the ngrok tunnel, and a
// repository's own pre-push hook posts an identical, identically signed payload straight
// to localhost. Both senders are wanted: the hook is what keeps notifications working
// when the free ngrok URL goes stale and the registered webhook address is dead. Only the
// first arrival should reach Telegram.
//
// Kept in memory rather than in the database. A restart may let one duplicate through,
// which is a smaller price than a schema, a migration and a table for state that stops
// being interesting after a few minutes.

const duplicateWindow = 10 * time.Minute

var (
	seenMu      sync.Mutex
	seenCommits = map[string]time.Time{}
)

// reserveAnnouncement bu commit hali e'lon qilinmaganini tekshiradi va uni
// band qilib qo'yadi. Band qilish — yuborishdan OLDIN, chunki ikkita nusxa
// (GitHub va lokal hook) bir vaqtda kelishi mumkin.
//
// Muvaffaqiyatsiz yuborishdan keyin releaseAnnouncement chaqirilishi shart:
// aks holda GitHub'dagi "Redeliver" tugmasi 10 daqiqa davomida "duplicate"
// deb rad etiladi va xabar butunlay yo'qoladi.
//
// Xotirada saqlanadi, bazada emas: qayta ishga tushganda bitta dublikat
// o'tib ketishi mumkin, bu esa bir necha daqiqadan keyin keraksiz bo'lib
// qoladigan holat uchun alohida jadval va migratsiyadan arzonroq.
func reserveAnnouncement(
	key string,
) bool {

	now := time.Now()

	seenMu.Lock()
	defer seenMu.Unlock()

	for k, at := range seenCommits {

		if now.Sub(at) > duplicateWindow {
			delete(seenCommits, k)
		}
	}

	if at, ok := seenCommits[key]; ok &&
		now.Sub(at) <= duplicateWindow {

		return false
	}

	seenCommits[key] = now

	return true
}

func releaseAnnouncement(
	key string,
) {

	seenMu.Lock()
	defer seenMu.Unlock()

	delete(seenCommits, key)
}

// webhookKey /webhook/github/<kalit> yo'lidan kalitni ajratib oladi.
// Eski /webhook/github yo'li uchun bo'sh satr qaytadi.
func webhookKey(path string) string {

	const prefix = "/webhook/github/"

	if !strings.HasPrefix(path, prefix) {
		return ""
	}

	return strings.Trim(
		strings.TrimPrefix(path, prefix),
		"/",
	)
}

func githubWebhook(
	w http.ResponseWriter,
	r *http.Request,
) {

	if r.Method != http.MethodPost {

		http.Error(
			w,
			"Method not allowed",
			http.StatusMethodNotAllowed,
		)

		return
	}

	// --------------------------------------------------------
	// READ RAW BODY
	// --------------------------------------------------------

	body, err := io.ReadAll(r.Body)

	if err != nil {

		http.Error(
			w,
			"Failed to read request body",
			http.StatusBadRequest,
		)

		return
	}

	// --------------------------------------------------------
	// KANAL KALITI
	// --------------------------------------------------------
	//
	// Kalit bo'lsa — imzo o'sha kalit bilan tekshiriladi va xabar o'sha
	// kalitga tegishli kanalga boradi. Kalitsiz so'rov eski umumiy sir
	// bilan tekshiriladi va kanal loyiha nomi bo'yicha topiladi.

	key := webhookKey(r.URL.Path)

	if key == "" {
		key = strings.TrimSpace(
			r.Header.Get("X-GitPulse-Key"),
		)
	}

	secret := config.GitHubWebhookSecret

	var keyedChannel *Channel

	if key != "" {

		channel, err := channelByKey(key)

		if err != nil {

			log.Println(
				"❌ Noma'lum kanal kaliti",
			)

			http.Error(
				w,
				"Unknown channel key",
				http.StatusUnauthorized,
			)

			return
		}

		keyedChannel = &channel
		secret = key
	}

	// --------------------------------------------------------
	// GITHUB SIGNATURE
	// --------------------------------------------------------

	signature := r.Header.Get(
		"X-Hub-Signature-256",
	)

	if !verifyGitHubSignature(
		body,
		signature,
		secret,
	) {

		log.Println(
			"❌ Invalid GitHub webhook signature",
		)

		http.Error(
			w,
			"Invalid webhook signature",
			http.StatusUnauthorized,
		)

		return
	}

	// --------------------------------------------------------
	// EVENT
	// --------------------------------------------------------

	event := r.Header.Get(
		"X-GitHub-Event",
	)

	log.Println("=== GitHub Webhook ===")
	log.Println("Event:", event)

	// --------------------------------------------------------
	// PING
	// --------------------------------------------------------

	if event == "ping" {

		log.Println(
			"🏓 GitHub ping received",
		)

		w.WriteHeader(http.StatusOK)

		_, _ = w.Write(
			[]byte("Ping received"),
		)

		return
	}

	// --------------------------------------------------------
	// ONLY PUSH
	// --------------------------------------------------------

	if event != "push" {

		log.Println(
			"Event ignored:",
			event,
		)

		w.WriteHeader(http.StatusOK)

		_, _ = w.Write(
			[]byte("Event ignored"),
		)

		return
	}

	// --------------------------------------------------------
	// PARSE JSON
	// --------------------------------------------------------

	var payload GitHubPayload

	if err := json.Unmarshal(
		body,
		&payload,
	); err != nil {

		http.Error(
			w,
			"Invalid JSON",
			http.StatusBadRequest,
		)

		return
	}

	// --------------------------------------------------------
	// HEAD COMMIT
	// --------------------------------------------------------

	if payload.HeadCommit == nil {

		w.WriteHeader(http.StatusOK)

		_, _ = w.Write(
			[]byte("No head commit"),
		)

		return
	}

	// --------------------------------------------------------
	// PROJECT
	// --------------------------------------------------------

	project := strings.TrimSpace(
		payload.Repository.Name,
	)

	if project == "" {

		http.Error(
			w,
			"Project name missing",
			http.StatusBadRequest,
		)

		return
	}

	githubURL := strings.TrimSpace(
		payload.Repository.HTMLURL,
	)

	// To'liq nom ("owner/repo") loyihani aniq ajratadi: turli egadagi bir xil
	// nomli repo'lar bir-birining kanaliga tushmaydi. Lokal hook uni
	// yubormasa, repo manzilidan tiklanadi.
	fullName := strings.TrimSpace(
		payload.Repository.FullName,
	)

	if fullName == "" {
		fullName = fullNameFromURL(githubURL)
	}

	// --------------------------------------------------------
	// REGISTER PROJECT
	// --------------------------------------------------------

	if err := registerProject(
		project,
		fullName,
		githubURL,
	); err != nil {

		log.Println(
			"❌ Project registration error:",
			err,
		)

		http.Error(
			w,
			"Database error",
			http.StatusInternalServerError,
		)

		return
	}

	// --------------------------------------------------------
	// BRANCH
	// --------------------------------------------------------

	branch := strings.TrimPrefix(
		payload.Ref,
		"refs/heads/",
	)

	commit := *payload.HeadCommit

	// --------------------------------------------------------
	// KANAL
	// --------------------------------------------------------

	channel, routed, err := routeFor(
		keyedChannel,
		fullName,
		project,
	)

	if err != nil {

		log.Println(
			"❌ Kanalni aniqlash xatosi:",
			err,
		)

		http.Error(
			w,
			"Database error",
			http.StatusInternalServerError,
		)

		return
	}

	destination := "default"

	if routed {
		destination = channel.ChatID
	}

	// --------------------------------------------------------
	// DUPLICATE
	// --------------------------------------------------------

	dedupKey :=
		destination +
			"@" + branch +
			"@" + commit.ID

	if !reserveAnnouncement(dedupKey) {

		log.Println(
			"🔁 Duplicate push ignored:",
			commit.ID,
		)

		w.WriteHeader(http.StatusOK)

		_, _ = w.Write(
			[]byte("Duplicate ignored"),
		)

		return
	}

	// --------------------------------------------------------
	// CHANGES
	// --------------------------------------------------------

	// Push ichidagi barcha commitlar hisobga olinadi, faqat oxirgisi emas.
	commits := payload.Commits

	if len(commits) == 0 {
		commits = []Commit{commit}
	}

	added, modified, removed := changeCounts(commits)

	// Deploy xabari "aynan qaysi o'zgarishlar chiqdi" deb yoza olishi
	// uchun har bir commit yozib qo'yiladi.
	deployKeyForProject := fullName

	if deployKeyForProject == "" {
		deployKeyForProject = project
	}

	recordPush(deployKeyForProject, branch, commits)

	totalFiles :=
		added +
			modified +
			removed

	// --------------------------------------------------------
	// FORMAT MESSAGE
	// --------------------------------------------------------

	message := formatTelegramMessage(
		project,
		branch,
		commit,
		commits,
		totalFiles,
		added,
		modified,
		removed,
	)

	// --------------------------------------------------------
	// LOG
	// --------------------------------------------------------

	log.Println(
		"📦 Project:",
		project,
	)

	log.Println(
		"🌿 Branch:",
		branch,
	)

	log.Println(
		"👤 Author:",
		commit.Author.Name,
	)

	log.Println(
		"📝 Commit:",
		commit.Message,
	)

	log.Println(
		"🔖 Commit ID:",
		commit.ID,
	)

	log.Println(
		"📣 Kanal:",
		destination,
	)

	// --------------------------------------------------------
	// SEND TELEGRAM
	// --------------------------------------------------------

	var sendErr error

	if routed {

		// Kanalda topic yo'q: xabar to'g'ridan-to'g'ri kanalga boradi.
		sendErr = sendRouted(
			channel,
			message,
		)

	} else {

		sendErr = sendProjectNotification(
			project,
			message,
		)
	}

	if sendErr != nil {

		// Band qilishni bo'shatamiz, aks holda GitHub'dagi "Redeliver"
		// 10 daqiqa davomida dublikat deb rad etiladi.
		releaseAnnouncement(dedupKey)

		log.Println(
			"❌ Telegram error:",
			sendErr,
		)

		http.Error(
			w,
			"Failed to send Telegram message",
			http.StatusInternalServerError,
		)

		return
	}

	log.Println(
		"✅ Telegram notification sent",
	)

	w.WriteHeader(http.StatusOK)

	_, _ = w.Write(
		[]byte("Webhook received"),
	)
}

// ============================================================
// YO'NALTIRISH
// ============================================================

// routeFor push qaysi kanalga tushishini aniqlaydi.
//
// Kalit bilan kelgan so'rov o'sha kalitning kanaliga boradi va bir vaqtning
// o'zida loyihani shu kanalga bog'lab qo'yadi — shundan keyin kalitsiz
// kelgan so'rov (masalan lokal pre-push hook) ham to'g'ri kanalga tushadi.
func routeFor(
	keyed *Channel,
	fullName string,
	name string,
) (Channel, bool, error) {

	if keyed != nil {

		target := fullName

		if target == "" {
			target = name
		}

		if err := bindProject(
			target,
			keyed.ChatID,
		); err != nil {

			log.Println(
				"⚠️ Loyihani kanalga bog'lab bo'lmadi:",
				err,
			)
		}

		return *keyed, true, nil
	}

	return channelForProject(fullName, name)
}

// fullNameFromURL "https://github.com/owner/repo" dan "owner/repo" ni oladi.
func fullNameFromURL(githubURL string) string {

	trimmed := strings.TrimSuffix(
		strings.TrimSuffix(githubURL, "/"),
		".git",
	)

	parts := strings.Split(trimmed, "/")

	if len(parts) < 2 {
		return ""
	}

	owner := parts[len(parts)-2]
	repo := parts[len(parts)-1]

	if owner == "" || repo == "" || strings.Contains(owner, ":") {
		return ""
	}

	return owner + "/" + repo
}

// changeCounts push ichidagi barcha commitlar bo'yicha o'zgargan fayllarni
// sanaydi. Bitta fayl bir necha commitda tegilgan bo'lsa, bir marta
// sanaladi — "12 fayl o'zgardi" deganda odam shuni tushunadi.
func changeCounts(
	commits []Commit,
) (int, int, int) {

	added := map[string]bool{}
	modified := map[string]bool{}
	removed := map[string]bool{}

	for _, commit := range commits {

		for _, path := range commit.Added {
			added[path] = true
		}

		for _, path := range commit.Modified {
			modified[path] = true
		}

		for _, path := range commit.Removed {
			removed[path] = true
		}
	}

	// Qo'shilgan keyin o'chirilgan fayl ikki marta sanalmasin.
	for path := range removed {
		delete(added, path)
		delete(modified, path)
	}

	for path := range added {
		delete(modified, path)
	}

	return len(added), len(modified), len(removed)
}

// ============================================================
// PROJECT REGISTRY
// ============================================================

func registerProject(
	project string,
	fullName string,
	githubURL string,
) error {

	// Repo nomi o'zgarsa ("sat-platform" → "Sat-Platform") jadvalda ikkita
	// qator paydo bo'lardi, chunki kalit registrga sezgir. Eski yozuvni
	// o'chirib, faqat oxirgi yozilishini qoldiramiz.
	if _, err := db.Exec(
		`DELETE FROM projects WHERE lower(name) = lower($1) AND name <> $1`,
		project,
	); err != nil {
		return err
	}

	query := `
		INSERT INTO projects (
			name,
			full_name,
			github_url
		)
		VALUES ($1, $2, $3)
		ON CONFLICT (name)
		DO UPDATE SET
			full_name = EXCLUDED.full_name,
			github_url = EXCLUDED.github_url,
			updated_at = NOW()
	`

	_, err := db.Exec(
		query,
		project,
		fullName,
		githubURL,
	)

	if err == nil {

		log.Println(
			"📦 Project registered:",
			project,
		)
	}

	return err
}

// ============================================================
// PROJECT MODEL
// ============================================================

type Project struct {
	Name      string
	GitHubURL string
}

// ============================================================
// GET PROJECTS
// ============================================================

func getProjects() ([]Project, error) {

	rows, err := db.Query(
		`
		SELECT
			name,
			github_url
		FROM projects
		ORDER BY name ASC
		`,
	)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var projects []Project

	for rows.Next() {

		var project Project

		if err := rows.Scan(
			&project.Name,
			&project.GitHubURL,
		); err != nil {

			return nil, err
		}

		projects = append(
			projects,
			project,
		)
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return projects, nil
}

// ============================================================
// GITHUB HMAC SECURITY
// ============================================================

func verifyGitHubSignature(
	body []byte,
	signature string,
	secret string,
) bool {

	if secret == "" {
		return false
	}

	if signature == "" {
		return false
	}

	const prefix = "sha256="

	if !strings.HasPrefix(
		signature,
		prefix,
	) {
		return false
	}

	receivedSignature :=
		strings.TrimPrefix(
			signature,
			prefix,
		)

	mac := hmac.New(
		sha256.New,
		[]byte(secret),
	)

	_, err := mac.Write(body)

	if err != nil {
		return false
	}

	expectedSignature :=
		hex.EncodeToString(
			mac.Sum(nil),
		)

	return hmac.Equal(
		[]byte(receivedSignature),
		[]byte(expectedSignature),
	)
}

// ============================================================
// TELEGRAM MESSAGE FORMAT
// ============================================================

// maxCommitMessageRunes is the budget for the commit body inside one notification.
// Telegram's hard limit is 4096 characters for the whole message; the rest of the
// notification (title, repository, branch, author, counts, timestamp, link) plus HTML
// escaping needs the remaining headroom.
const maxCommitMessageRunes = 1500

// truncateRunes shortens s to at most n runes, appending an ellipsis when it cuts.
// It counts runes rather than bytes so a multi-byte character is never split in half,
// which would produce invalid UTF-8 and be rejected.
func truncateRunes(
	s string,
	n int,
) string {

	r := []rune(s)

	if len(r) <= n {
		return s
	}

	return string(r[:n]) + "..."
}

// maxCommitBodyRunes — commit tanasining chegarasi.
//
// Tana butunlay tashlanmaydi: "aynan nima o'zgardi va qanday muammo hal
// qilindi" degan javob aynan o'sha yerda yozilgan bo'ladi. Lekin uzun
// "Tekshirilgan:" ro'yxatlari kartochkani devorga aylantirib yuborardi,
// shuning uchun chegara bor.
const maxCommitBodyRunes = 600

// maxBodyLines — tanadan ko'rsatiladigan qatorlar soni.
const maxBodyLines = 10

// maxListedFiles — kartochkada sanab o'tiladigan fayllar soni.
const maxListedFiles = 12

// localTime vaqtni Toshkent vaqtiga o'tkazadi.
func localTime(t time.Time) time.Time {

	loc, err := time.LoadLocation("Asia/Tashkent")

	if err != nil {
		return t
	}

	return t.In(loc)
}

// splitCommitMessage commit xabarini sarlavha va tanaga bo'ladi.
//
// Trailerlar (Co-Authored-By va shunga o'xshashlar) tashlanadi: ular
// kartochkada joy egallaydi, lekin o'zgarish haqida hech narsa aytmaydi.
func splitCommitMessage(
	message string,
) (string, string) {

	message = strings.TrimSpace(message)

	if message == "" {
		return "", ""
	}

	lines := strings.Split(
		strings.ReplaceAll(message, "\r\n", "\n"),
		"\n",
	)

	subject := strings.TrimSpace(lines[0])

	var body []string

	for _, line := range lines[1:] {

		trimmed := strings.TrimSpace(line)
		lower := strings.ToLower(trimmed)

		if strings.HasPrefix(lower, "co-authored-by:") ||
			strings.HasPrefix(lower, "signed-off-by:") ||
			strings.HasPrefix(lower, "reviewed-by:") ||
			strings.HasPrefix(lower, "🤖 generated with") {

			continue
		}

		body = append(body, line)
	}

	// Boshidagi va oxiridagi bo'sh qatorlarni olib tashlaymiz.
	for len(body) > 0 && strings.TrimSpace(body[0]) == "" {
		body = body[1:]
	}

	for len(body) > 0 && strings.TrimSpace(body[len(body)-1]) == "" {
		body = body[:len(body)-1]
	}

	if len(body) > maxBodyLines {
		body = append(body[:maxBodyLines], "…")
	}

	return subject, strings.Join(body, "\n")
}

// changedFilesBlock o'zgargan fayllarni yopiq ro'yxat qilib qaytaradi.
//
// "20 fayl o'zgardi" degani hech narsa aytmaydi; qaysi fayllar ekani esa
// o'zgarishning nimaga tegishini bir qarashda ko'rsatadi. Ro'yxat
// yig'ilgan holda turadi, shuning uchun kartochka uzaymaydi.
func changedFilesBlock(
	commits []Commit,
) string {

	type entry struct {
		icon string
		path string
	}

	seen := map[string]bool{}

	var entries []entry

	add := func(icon string, paths []string) {

		for _, path := range paths {

			if seen[path] {
				continue
			}

			seen[path] = true

			entries = append(entries, entry{icon: icon, path: path})
		}
	}

	for _, commit := range commits {
		add("🟢", commit.Added)
	}

	for _, commit := range commits {
		add("🔴", commit.Removed)
	}

	for _, commit := range commits {
		add("🟡", commit.Modified)
	}

	if len(entries) == 0 {
		return ""
	}

	hidden := 0

	if len(entries) > maxListedFiles {
		hidden = len(entries) - maxListedFiles
		entries = entries[:maxListedFiles]
	}

	var builder strings.Builder

	builder.WriteString("<blockquote expandable>")

	for _, item := range entries {

		builder.WriteString(
			item.icon +
				" <code>" +
				html.EscapeString(truncateRunes(item.path, 70)) +
				"</code>\n",
		)
	}

	if hidden > 0 {
		builder.WriteString(
			fmt.Sprintf("… +%d ta fayl\n", hidden),
		)
	}

	builder.WriteString("</blockquote>")

	return builder.String()
}

func formatTelegramMessage(
	project string,
	branch string,
	commit Commit,
	commits []Commit,
	totalFiles int,
	added int,
	modified int,
	removed int,
) string {

	author := strings.TrimSpace(commit.Author.Name)

	if author == "" {
		author = "Unknown"
	}

	subject, body := splitCommitMessage(commit.Message)

	if subject == "" {
		subject = "(commit xabari yo'q)"
	}

	// Telegram 4096 belgidan uzun xabarni qabul qilmaydi. Qisqartirish
	// escape qilishdan OLDIN bajariladi: html.EscapeString bitta belgini
	// beshtagacha cho'zishi mumkin, keyin qisqartirsak chegaradan
	// oshib ketishimiz mumkin edi.
	subject = truncateRunes(subject, 200)
	body = truncateRunes(body, maxCommitBodyRunes)

	commitTime := commit.Timestamp

	if parsed, err := time.Parse(
		time.RFC3339,
		commit.Timestamp,
	); err == nil {
		commitTime = localTime(parsed).Format("02.01.2006 15:04")
	}

	var builder strings.Builder

	// Push va deploy xabarlari bitta kanalda yonma-yon turadi, shuning
	// uchun birinchi qatorning o'zidayoq qaysi biri ekani ko'rinishi
	// kerak.
	builder.WriteString("🟡 <b>COMMIT · PUSH</b>\n")
	builder.WriteString("━━━━━━━━━━━━━━━\n\n")

	builder.WriteString(
		"📦 <b>" + html.EscapeString(project) + "</b>" +
			" · 🌿 <code>" + html.EscapeString(branch) + "</code>\n",
	)

	builder.WriteString(
		"👤 " + html.EscapeString(author) +
			" · 🔖 <code>" + html.EscapeString(shortSHA(commit.ID)) + "</code>\n\n",
	)

	builder.WriteString(
		"📝 <b>" + html.EscapeString(subject) + "</b>\n",
	)

	if body != "" {
		builder.WriteString(
			"\n" + html.EscapeString(body) + "\n",
		)
	}

	builder.WriteString(otherCommitsBlock(commit, commits))

	builder.WriteString(
		fmt.Sprintf(
			"\n📊 🟢 %d · 🟡 %d · 🔴 %d — %d fayl\n",
			added,
			modified,
			removed,
			totalFiles,
		),
	)

	if files := changedFilesBlock(commits); files != "" {
		builder.WriteString(files + "\n")
	}

	builder.WriteString("\n🕐 " + html.EscapeString(commitTime) + "\n")

	if commit.URL != "" {
		builder.WriteString(
			"\n🔗 <a href=\"" +
				html.EscapeString(commit.URL) +
				"\">Commit'ni ko'rish</a>",
		)
	}

	return builder.String()
}

// maxListedCommits — bitta xabarda sanab o'tiladigan qo'shimcha commitlar
// soni. Qolganlari "+N ta yana" bo'lib ko'rsatiladi.
const maxListedCommits = 5

// otherCommitsBlock bitta push'da bir nechta commit bo'lganda qolganlarini
// qisqa ro'yxat qilib qaytaradi. Bitta commit bo'lsa — bo'sh satr.
//
// Har bir commit uchun alohida xabar yuborilmaydi: o'nta commitli push
// kanalni o'nta kartochka bilan to'ldirib yuborardi.
func otherCommitsBlock(
	head Commit,
	commits []Commit,
) string {

	var others []Commit

	for _, c := range commits {
		if c.ID != head.ID {
			others = append(others, c)
		}
	}

	if len(others) == 0 {
		return ""
	}

	// GitHub commitlarni eskidan yangiga qarab yuboradi; oxirgisi head.
	// Ro'yxat yangidan eskiga qarab o'qilgani qulayroq.
	for i, j := 0, len(others)-1; i < j; i, j = i+1, j-1 {
		others[i], others[j] = others[j], others[i]
	}

	hidden := 0

	if len(others) > maxListedCommits {
		hidden = len(others) - maxListedCommits
		others = others[:maxListedCommits]
	}

	var builder strings.Builder

	builder.WriteString(
		fmt.Sprintf(
			"\n📚 <b>Shu push'dagi yana %d commit</b>\n",
			len(others)+hidden,
		),
	)

	for _, c := range others {

		shortID := c.ID

		if len(shortID) > 7 {
			shortID = shortID[:7]
		}

		subject := strings.TrimSpace(c.Message)

		if index := strings.IndexAny(subject, "\r\n"); index >= 0 {
			subject = subject[:index]
		}

		subject = truncateRunes(subject, 80)

		if subject == "" {
			subject = "(xabarsiz)"
		}

		builder.WriteString(
			fmt.Sprintf(
				"• <code>%s</code> %s\n",
				html.EscapeString(shortID),
				html.EscapeString(subject),
			),
		)
	}

	if hidden > 0 {

		builder.WriteString(
			fmt.Sprintf("• +%d ta yana\n", hidden),
		)
	}

	return builder.String()
}

// ============================================================
// TELEGRAM SEND TO CHAT
// ============================================================

func sendTelegramMessageToChat(
	chatID string,
	message string,
	showKeyboard bool,
) error {

	data := url.Values{}

	data.Set(
		"chat_id",
		chatID,
	)

	data.Set(
		"text",
		message,
	)

	data.Set(
		"parse_mode",
		"HTML",
	)

	data.Set(
		"disable_web_page_preview",
		"true",
	)

	// --------------------------------------------------------
	// REPLY KEYBOARD
	// --------------------------------------------------------

	if showKeyboard {

		keyboard := map[string]interface{}{
			"keyboard": [][]map[string]string{
				{
					{
						"text": "📦 Projects",
					},
					{
						"text": "📣 Channels",
					},
					{
						"text": "🛠 Help",
					},
				},
			},
			"resize_keyboard":   true,
			"one_time_keyboard": false,
		}

		keyboardJSON, err :=
			json.Marshal(keyboard)

		if err != nil {
			return err
		}

		data.Set(
			"reply_markup",
			string(keyboardJSON),
		)
	}

	// Bitta Telegram klienti ishlatiladi: xato formati, timeout va
	// 429 (rate limit) ishlovi hamma joyda bir xil bo'lsin.
	return telegramCall("sendMessage", data, nil)
}

// ============================================================
// TELEGRAM BOT
// ============================================================

// redactToken matndan bot tokenini olib tashlaydi.
//
// net/http xatolari butun URL'ni o'z ichiga oladi, token esa URL ichida
// turadi — ya'ni oddiy tarmoq uzilishi ham tokenni terminalga va log
// fayliga chiqarib yuborardi.
func redactToken(text string) string {

	if config.TelegramBotToken == "" {
		return text
	}

	return strings.ReplaceAll(
		text,
		config.TelegramBotToken,
		"<token>",
	)
}

func startTelegramBot() {

	log.Println(
		"🤖 Telegram bot started",
	)

	offset := int64(0)

	for {

		updates, err :=
			getTelegramUpdates(
				offset,
			)

		if err != nil {

			log.Println(
				"❌ Telegram polling error:",
				redactToken(err.Error()),
			)

			time.Sleep(
				3 * time.Second,
			)

			continue
		}

		for _, update := range updates {

			offset =
				update.UpdateID + 1

			switch {

			case update.MyChatMember != nil:

				handleMyChatMember(
					update.MyChatMember,
				)

			case update.ChannelPost != nil:

				handleChannelPost(
					update.ChannelPost,
				)

			case update.Message != nil:

				handleTelegramMessage(
					update.Message,
				)
			}
		}
	}
}

// ============================================================
// TELEGRAM GET UPDATES
// ============================================================

func getTelegramUpdates(
	offset int64,
) ([]TelegramUpdate, error) {

	apiURL :=
		"https://api.telegram.org/bot" +
			config.TelegramBotToken +
			"/getUpdates"

	params := url.Values{}

	params.Set(
		"timeout",
		"30",
	)

	params.Set(
		"offset",
		strconv.FormatInt(
			offset,
			10,
		),
	)

	// my_chat_member — bot kanalga admin qilinganini bilish uchun; uni
	// aniq so'ramasak ham Telegram yuboradi, lekin ro'yxatni ochiq yozib
	// qo'ygan ma'qul: keyin yangi tur qo'shilsa, nega kelmayotgani
	// darrov ko'rinadi.
	params.Set(
		"allowed_updates",
		`["message","channel_post","my_chat_member"]`,
	)

	client := &http.Client{
		Timeout: 50 * time.Second,
	}

	resp, err :=
		client.Get(
			apiURL +
				"?" +
				params.Encode(),
		)

	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {

		body, _ :=
			io.ReadAll(resp.Body)

		return nil, fmt.Errorf(
			"Telegram returned %s: %s",
			resp.Status,
			string(body),
		)
	}

	var response struct {
		OK     bool             `json:"ok"`
		Result []TelegramUpdate `json:"result"`
	}

	if err := json.NewDecoder(
		resp.Body,
	).Decode(&response); err != nil {

		return nil, err
	}

	if !response.OK {

		return nil, fmt.Errorf(
			"Telegram API returned ok=false",
		)
	}

	return response.Result, nil
}

// ============================================================
// DELETE TELEGRAM WEBHOOK
// ============================================================

func deleteTelegramWebhook() error {

	apiURL :=
		"https://api.telegram.org/bot" +
			config.TelegramBotToken +
			"/deleteWebhook"

	resp, err :=
		http.PostForm(
			apiURL,
			url.Values{},
		)

	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {

		body, _ :=
			io.ReadAll(resp.Body)

		return fmt.Errorf(
			"Telegram webhook delete failed: %s: %s",
			resp.Status,
			string(body),
		)
	}

	return nil
}

// ============================================================
// TELEGRAM COMMAND HANDLER
// ============================================================

func handleTelegramMessage(
	message *TelegramMessage,
) {

	// Bot faqat shaxsiy chatda gapiradi.
	//
	// Guruhda javob berish ikkita yomon natija berardi: "Samandar Hodiev
	// added you" kabi matnsiz xizmat xabariga "Unknown command" deb javob
	// qaytarardi, va reply keyboard guruhdagi hamma uchun osilib qolardi.
	// Guruh va kanal faqat push xabarlarini oladi.
	if message.Chat.Type != "" &&
		message.Chat.Type != "private" {

		return
	}

	text :=
		strings.TrimSpace(
			message.Text,
		)

	// Matnsiz xabar (rasm, xizmat xabari) buyruq emas.
	if text == "" {
		return
	}

	log.Println(
		"📩 Telegram message:",
		text,
	)

	fields := strings.Fields(text)

	command := ""
	args := []string{}

	if len(fields) > 0 {
		command = strings.ToLower(fields[0])
		command = strings.SplitN(command, "@", 2)[0]
		args = fields[1:]
	}

	switch command {

	case "/channels":

		handleChannelsCommand(message)

		return

	case "/bind":

		handleBindCommand(message, args)

		return

	case "/unbind":

		handleUnbindCommand(message, args)

		return
	}

	command =
		strings.ToLower(text)

	switch command {

	case "/start":

		handleStartCommand(
			message,
		)

	case "/help":

		handleHelpCommand(
			message,
		)

	case "/projects":

		handleProjectsCommand(
			message,
		)

	case "📦 projects":

		handleProjectsCommand(
			message,
		)

	case "📣 channels":

		handleChannelsCommand(
			message,
		)

	case "🛠 help":

		handleHelpCommand(
			message,
		)

	default:

		_ = sendTelegramMessageToChat(
			strconv.FormatInt(
				message.Chat.ID,
				10,
			),
			"❓ Unknown command.\n\n"+
				"Use the buttons below or /help.",
			true,
		)
	}
}

// ============================================================
// /START
// ============================================================

func handleStartCommand(
	message *TelegramMessage,
) {

	chatID :=
		strconv.FormatInt(
			message.Chat.ID,
			10,
		)

	text :=
		"🚀 <b>GitPulse</b>\n\n" +
			"GitHub repository monitoring bot.\n\n" +
			"📡 GitHub push va commit'larni " +
			"real-time Telegram notification'ga aylantiradi.\n\n" +
			"📦 <b>Features</b>\n" +
			"• GitHub Webhook\n" +
			"• Multi-project\n" +
			"• Webhook Security\n" +
			"• PostgreSQL\n\n" +
			"🛠 <b>Commands</b>\n" +
			"/projects\n" +
			"/help"

	if err := sendTelegramMessageToChat(
		chatID,
		text,
		true,
	); err != nil {

		log.Println(
			"/start error:",
			err,
		)
	}
}

// ============================================================
// /HELP
// ============================================================

func handleHelpCommand(
	message *TelegramMessage,
) {

	chatID :=
		strconv.FormatInt(
			message.Chat.ID,
			10,
		)

	text :=
		"🛠 <b>GitPulse Help</b>\n\n" +
			"📦 <b>/projects</b> — loyihalar ro'yxati\n" +
			"📣 <b>/channels</b> — ulangan kanallar va kalitlari\n" +
			"🔗 <b>/bind &lt;loyiha&gt; &lt;raqam&gt;</b> — loyihani kanalga bog'lash\n" +
			"✂️ <b>/unbind &lt;loyiha&gt;</b> — bog'lanishni uzish\n\n" +
			"<b>Yangi loyihani ulash</b>\n" +
			"1. Kanal oching va botni admin qiling\n" +
			"2. Kalit shu yerga keladi (yoki kanalda <code>/connect</code> yozing)\n" +
			"3. Kalitni GitHub webhook'iga yoki lokal hook'ga qo'ying\n\n" +
			"Har bir loyihaning push'lari faqat o'z kanaliga tushadi.\n\n" +
			"🔐 Webhook HMAC-SHA256 bilan himoyalangan."

	if err := sendTelegramMessageToChat(
		chatID,
		text,
		true,
	); err != nil {

		log.Println(
			"/help error:",
			err,
		)
	}
}

// ============================================================
// /PROJECTS
// ============================================================

func handleProjectsCommand(
	message *TelegramMessage,
) {

	chatID :=
		strconv.FormatInt(
			message.Chat.ID,
			10,
		)

	projectList, err :=
		getProjects()

	if err != nil {

		log.Println(
			"❌ /projects database error:",
			err,
		)

		_ = sendTelegramMessageToChat(
			chatID,
			"❌ Database error:\n"+
				"<code>"+
				html.EscapeString(
					err.Error(),
				)+
				"</code>",
			true,
		)

		return
	}

	if len(projectList) == 0 {

		_ = sendTelegramMessageToChat(
			chatID,
			"🚀 <b>GitPulse</b>\n\n"+
				"📦 <b>Projects</b>\n\n"+
				"No projects registered yet.",
			true,
		)

		return
	}

	var builder strings.Builder

	builder.WriteString(
		"🚀 <b>GitPulse</b>\n\n",
	)

	builder.WriteString(
		"📦 <b>Projects</b>\n\n",
	)

	for _, project := range projectList {

		name :=
			html.EscapeString(
				project.Name,
			)

		builder.WriteString(
			fmt.Sprintf(
				"📦 <b>%s</b>\n",
				name,
			),
		)

		if project.GitHubURL != "" {

			githubURL :=
				html.EscapeString(
					project.GitHubURL,
				)

			builder.WriteString(
				fmt.Sprintf(
					"🔗 <a href=\"%s\">GitHub</a>\n",
					githubURL,
				),
			)
		}

		builder.WriteString("\n")
	}

	builder.WriteString(
		fmt.Sprintf(
			"📊 <b>Total:</b> %d projects",
			len(projectList),
		),
	)

	if err := sendTelegramMessageToChat(
		chatID,
		builder.String(),
		true,
	); err != nil {

		log.Println(
			"/projects Telegram error:",
			err,
		)
	}
}
