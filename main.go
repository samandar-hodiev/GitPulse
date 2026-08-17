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
		Name    string `json:"name"`
		HTMLURL string `json:"html_url"`
	} `json:"repository"`

	HeadCommit *Commit `json:"head_commit"`
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
	UpdateID int64             `json:"update_id"`
	Message  *TelegramMessage  `json:"message"`
}

type TelegramMessage struct {
	MessageID int64        `json:"message_id"`
	Chat      TelegramChat `json:"chat"`
	Text      string       `json:"text"`
}

type TelegramChat struct {
	ID   int64  `json:"id"`
	Type string `json:"type"`
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
}

var config Config

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

	http.HandleFunc("/health", health)
	http.HandleFunc("/webhook/github", githubWebhook)

	// --------------------------------------------------------
	// TELEGRAM BOT
	// --------------------------------------------------------

	go startTelegramBot()

	// --------------------------------------------------------
	// SERVER
	// --------------------------------------------------------

	fmt.Printf(
		"⚡ GitPulse server started on :%s\n",
		config.Port,
	)

	log.Fatal(
		http.ListenAndServe(
			":"+config.Port,
			nil,
		),
	)
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
	`

	if _, err := db.Exec(migration); err != nil {
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
	// GITHUB SIGNATURE
	// --------------------------------------------------------

	signature := r.Header.Get(
		"X-Hub-Signature-256",
	)

	if !verifyGitHubSignature(
		body,
		signature,
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

	// --------------------------------------------------------
	// REGISTER PROJECT
	// --------------------------------------------------------

	if err := registerProject(
		project,
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
	// CHANGES
	// --------------------------------------------------------

	added := len(commit.Added)
	modified := len(commit.Modified)
	removed := len(commit.Removed)

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

	// --------------------------------------------------------
	// SEND TELEGRAM
	// --------------------------------------------------------

	if err := sendTelegramMessage(
		message,
	); err != nil {

		log.Println(
			"❌ Telegram error:",
			err,
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
// PROJECT REGISTRY
// ============================================================

func registerProject(
	project string,
	githubURL string,
) error {

	query := `
		INSERT INTO projects (
			name,
			github_url
		)
		VALUES ($1, $2)
		ON CONFLICT (name)
		DO UPDATE SET
			github_url = EXCLUDED.github_url,
			updated_at = NOW()
	`

	_, err := db.Exec(
		query,
		project,
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
) bool {

	secret :=
		config.GitHubWebhookSecret

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

func formatTelegramMessage(
	project string,
	branch string,
	commit Commit,
	totalFiles int,
	added int,
	modified int,
	removed int,
) string {

	project =
		html.EscapeString(project)

	branch =
		html.EscapeString(branch)

	author :=
		strings.TrimSpace(
			commit.Author.Name,
		)

	if author == "" {
		author = "Unknown"
	}

	author =
		html.EscapeString(author)

	commitMessage :=
		strings.TrimSpace(
			commit.Message,
		)

	if commitMessage == "" {
		commitMessage =
			"No commit message"
	}

	commitMessage =
		html.EscapeString(
			commitMessage,
		)

	commitID := commit.ID

	if len(commitID) > 7 {
		commitID =
			commitID[:7]
	}

	commitID =
		html.EscapeString(commitID)

	commitURL :=
		html.EscapeString(
			commit.URL,
		)

	commitTime :=
		commit.Timestamp

	if parsedTime, err :=
		time.Parse(
			time.RFC3339,
			commit.Timestamp,
		); err == nil {

		loc, err :=
			time.LoadLocation(
				"Asia/Tashkent",
			)

		if err == nil {
			parsedTime =
				parsedTime.In(loc)
		}

		commitTime =
			parsedTime.Format(
				"02.01.2006 15:04",
			)
	}

	fileText :=
		"files changed"

	if totalFiles == 1 {
		fileText =
			"file changed"
	}

	return fmt.Sprintf(
		"🚀 <b>PUSH</b>\n\n"+
			"📦 <b>%s</b>\n"+
			"🌿 <code>%s</code>\n\n"+
			"👤 %s\n\n"+
			"📝 <b>%s</b>\n"+
			"🔖 <code>%s</code>\n\n"+
			"📊 <b>Changes</b>\n"+
			"🟢 %d added  •  "+
			"🟡 %d modified  •  "+
			"🔴 %d removed\n"+
			"📁 %d %s\n\n"+
			"🕐 %s\n\n"+
			"🔗 <a href=\"%s\">"+
			"View commit on GitHub"+
			"</a>",

		project,
		branch,
		author,
		commitMessage,
		commitID,
		added,
		modified,
		removed,
		totalFiles,
		fileText,
		commitTime,
		commitURL,
	)
}

// ============================================================
// TELEGRAM SEND
// ============================================================

func sendTelegramMessage(
	message string,
) error {

	return sendTelegramMessageToChat(
		config.TelegramChatID,
		message,
		false,
	)
}

// ============================================================
// TELEGRAM SEND TO CHAT
// ============================================================

func sendTelegramMessageToChat(
	chatID string,
	message string,
	showKeyboard bool,
) error {

	apiURL :=
		"https://api.telegram.org/bot" +
			config.TelegramBotToken +
			"/sendMessage"

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
						"text": "🛠 Help",
					},
				},
			},
			"resize_keyboard":  true,
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

	client := &http.Client{
		Timeout: 50 * time.Second,
	}

	resp, err :=
		client.PostForm(
			apiURL,
			data,
		)

	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {

		body, _ :=
			io.ReadAll(resp.Body)

		return fmt.Errorf(
			"Telegram returned %s: %s",
			resp.Status,
			string(body),
		)
	}

	return nil
}

// ============================================================
// TELEGRAM BOT
// ============================================================

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
				err,
			)

			time.Sleep(
				3 * time.Second,
			)

			continue
		}

		for _, update :=
			range updates {

			offset =
				update.UpdateID + 1

			if update.Message == nil {
				continue
			}

			handleTelegramMessage(
				update.Message,
			)
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
		OK     bool              `json:"ok"`
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

	text :=
		strings.TrimSpace(
			message.Text,
		)

	log.Println(
		"📩 Telegram message:",
		text,
	)

	command :=
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
		"🚀 <b>GitPulse</b>\n\n"+
			"GitHub repository monitoring bot.\n\n"+
			"📡 GitHub push va commit'larni "+
			"real-time Telegram notification'ga aylantiradi.\n\n"+
			"📦 <b>Features</b>\n"+
			"• GitHub Webhook\n"+
			"• Multi-project\n"+
			"• Webhook Security\n"+
			"• PostgreSQL\n\n"+
			"🛠 <b>Commands</b>\n"+
			"/projects\n"+
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
		"🛠 <b>GitPulse Help</b>\n\n"+
			"📦 <b>/projects</b>\n"+
			"GitHub projectlar ro'yxatini ko'rsatadi.\n\n"+
			"🔔 GitHub'dagi push'lar avtomatik ravishda "+
			"Telegramga notification bo'lib keladi.\n\n"+
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

	for _, project :=
		range projectList {

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