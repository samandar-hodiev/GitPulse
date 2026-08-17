package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/joho/godotenv"
)

// ==============================
// GITHUB TYPES
// ==============================

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

// ==============================
// TELEGRAM TYPES
// ==============================

type TelegramUpdate struct {
	UpdateID int `json:"update_id"`
	Message  *TelegramMessage `json:"message"`
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

// ==============================
// PROJECT STORAGE
// ==============================

// Hozircha projectlar RAM'da saqlanadi.
// Database keyin qo'shiladi.

var (
	projects   = make(map[string]bool)
	projectsMu sync.RWMutex
)

// ==============================
// MAIN
// ==============================

func main() {
	// Load .env
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env file not found")
	}

	// Required environment variables
	if os.Getenv("TELEGRAM_BOT_TOKEN") == "" {
		log.Fatal("TELEGRAM_BOT_TOKEN is missing")
	}

	if os.Getenv("TELEGRAM_CHAT_ID") == "" {
		log.Fatal("TELEGRAM_CHAT_ID is missing")
	}

	if os.Getenv("GITHUB_WEBHOOK_SECRET") == "" {
		log.Fatal("GITHUB_WEBHOOK_SECRET is missing")
	}

	// HTTP routes
	http.HandleFunc("/health", health)
	http.HandleFunc("/webhook/github", githubWebhook)

	// Telegram bot polling
	go startTelegramBot()

	fmt.Println("⚡ GitPulse server started on :8080")

	log.Fatal(
		http.ListenAndServe(":8080", nil),
	)
}

// ==============================
// HEALTH CHECK
// ==============================

func health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(
			w,
			"Method not allowed",
			http.StatusMethodNotAllowed,
		)

		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("GitPulse is running"))
}

// ==============================
// GITHUB WEBHOOK
// ==============================

func githubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(
			w,
			"Method not allowed",
			http.StatusMethodNotAllowed,
		)

		return
	}

	// Request body'ni raw holatda o'qiymiz.
	// HMAC signature tekshirish uchun kerak.
	body, err := io.ReadAll(r.Body)

	if err != nil {
		http.Error(
			w,
			"Failed to read request body",
			http.StatusBadRequest,
		)

		return
	}

	// GitHub signature
	signature := r.Header.Get(
		"X-Hub-Signature-256",
	)

	// Security check
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

	event := r.Header.Get(
		"X-GitHub-Event",
	)

	fmt.Println("=== GitHub Webhook ===")
	fmt.Println("Event:", event)

	// ==============================
	// PING EVENT
	// ==============================

	if event == "ping" {
		fmt.Println(
			"🏓 GitHub ping received",
		)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Ping received"))

		return
	}

	// ==============================
	// EVENT FILTER
	// ==============================

	// Hozircha faqat push event kerak.
	if event != "push" {
		fmt.Println(
			"Event ignored:",
			event,
		)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Event ignored"))

		return
	}

	// ==============================
	// PARSE PAYLOAD
	// ==============================

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

	// Empty commit check
	if payload.HeadCommit == nil {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("No head commit"))

		return
	}

	// ==============================
	// PROJECT
	// ==============================

	project := payload.Repository.Name

	// Projectni registry'ga qo'shamiz.
	registerProject(project)

	// ==============================
	// BRANCH
	// ==============================

	branch := strings.TrimPrefix(
		payload.Ref,
		"refs/heads/",
	)

	// ==============================
	// COMMIT
	// ==============================

	commit := *payload.HeadCommit

	// ==============================
	// CHANGES
	// ==============================

	added := len(commit.Added)
	modified := len(commit.Modified)
	removed := len(commit.Removed)

	totalFiles := added + modified + removed

	// ==============================
	// TELEGRAM MESSAGE
	// ==============================

	message := formatTelegramMessage(
		project,
		branch,
		commit,
		totalFiles,
		added,
		modified,
		removed,
	)

	// ==============================
	// LOG
	// ==============================

	fmt.Println(
		"Project:",
		project,
	)

	fmt.Println(
		"Branch:",
		branch,
	)

	fmt.Println(
		"Author:",
		commit.Author.Name,
	)

	fmt.Println(
		"Commit:",
		commit.Message,
	)

	fmt.Println(
		"Commit ID:",
		commit.ID,
	)

	// ==============================
	// SEND TELEGRAM
	// ==============================

	if err := sendTelegramMessage(
		message,
	); err != nil {

		log.Println(
			"Telegram error:",
			err,
		)

		http.Error(
			w,
			"Failed to send Telegram message",
			http.StatusInternalServerError,
		)

		return
	}

	w.WriteHeader(http.StatusOK)
	w.Write([]byte("Webhook received"))
}

// ==============================
// PROJECT REGISTRY
// ==============================

func registerProject(project string) {
	project = strings.TrimSpace(project)

	if project == "" {
		return
	}

	projectsMu.Lock()
	defer projectsMu.Unlock()

	projects[project] = true

	fmt.Println(
		"📦 Project registered:",
		project,
	)
}

// ==============================
// GITHUB SIGNATURE
// ==============================

func verifyGitHubSignature(
	body []byte,
	signature string,
) bool {

	secret := os.Getenv(
		"GITHUB_WEBHOOK_SECRET",
	)

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

	receivedSignature := strings.TrimPrefix(
		signature,
		prefix,
	)

	// HMAC-SHA256
	mac := hmac.New(
		sha256.New,
		[]byte(secret),
	)

	_, err := mac.Write(body)

	if err != nil {
		return false
	}

	expectedSignature := hex.EncodeToString(
		mac.Sum(nil),
	)

	// Constant-time comparison.
	return hmac.Equal(
		[]byte(receivedSignature),
		[]byte(expectedSignature),
	)
}

// ==============================
// TELEGRAM MESSAGE FORMAT
// ==============================

func formatTelegramMessage(
	project string,
	branch string,
	commit Commit,
	totalFiles int,
	added int,
	modified int,
	removed int,
) string {

	// ==============================
	// ESCAPE DATA
	// ==============================

	project = html.EscapeString(
		project,
	)

	branch = html.EscapeString(
		branch,
	)

	// ==============================
	// AUTHOR
	// ==============================

	author := strings.TrimSpace(
		commit.Author.Name,
	)

	if author == "" {
		author = "Unknown"
	}

	author = html.EscapeString(
		author,
	)

	// ==============================
	// COMMIT MESSAGE
	// ==============================

	commitMessage := strings.TrimSpace(
		commit.Message,
	)

	if commitMessage == "" {
		commitMessage = "No commit message"
	}

	commitMessage = html.EscapeString(
		commitMessage,
	)

	// ==============================
	// COMMIT ID
	// ==============================

	commitID := commit.ID

	if len(commitID) > 7 {
		commitID = commitID[:7]
	}

	commitID = html.EscapeString(
		commitID,
	)

	// ==============================
	// COMMIT URL
	// ==============================

	commitURL := html.EscapeString(
		commit.URL,
	)

	// ==============================
	// TIME
	// ==============================

	commitTime := commit.Timestamp

	if parsedTime, err := time.Parse(
		time.RFC3339,
		commit.Timestamp,
	); err == nil {

		loc, err := time.LoadLocation(
			"Asia/Tashkent",
		)

		if err == nil {
			parsedTime = parsedTime.In(loc)
		}

		commitTime = parsedTime.Format(
			"02.01.2006 15:04",
		)
	}

	// ==============================
	// FILE TEXT
	// ==============================

	fileText := "files changed"

	if totalFiles == 1 {
		fileText = "file changed"
	}

	// ==============================
	// FINAL MESSAGE
	// ==============================

	return fmt.Sprintf(
		"🚀 <b>PUSH</b>\n\n"+
			"📦 <b>%s</b>\n"+
			"🌿 <code>%s</code>\n\n"+
			"👤 %s\n\n"+
			"📝 <b>%s</b>\n"+
			"🔖 <code>%s</code>\n\n"+
			"📊 <b>Changes</b>\n"+
			"🟢 %d added  •  🟡 %d modified  •  🔴 %d removed\n"+
			"📁 %d %s\n\n"+
			"🕐 %s\n\n"+
			"🔗 <a href=\"%s\">View commit on GitHub</a>",

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

// ==============================
// TELEGRAM SEND MESSAGE
// ==============================

func sendTelegramMessage(
	message string,
) error {

	chatID := os.Getenv(
		"TELEGRAM_CHAT_ID",
	)

	if chatID == "" {
		return fmt.Errorf(
			"TELEGRAM_CHAT_ID is missing",
		)
	}

	return sendTelegramMessageToChat(
		chatID,
		message,
	)
}

// ==============================
// SEND TO SPECIFIC CHAT
// ==============================

func sendTelegramMessageToChat(
	chatID string,
	message string,
) error {

	token := os.Getenv(
		"TELEGRAM_BOT_TOKEN",
	)

	if token == "" {
		return fmt.Errorf(
			"TELEGRAM_BOT_TOKEN is missing",
		)
	}

	if chatID == "" {
		return fmt.Errorf(
			"chat ID is missing",
		)
	}

	apiURL :=
		"https://api.telegram.org/bot" +
			token +
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

	// Link preview'ni o'chiramiz.
	data.Set(
		"disable_web_page_preview",
		"true",
	)

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	resp, err := client.PostForm(
		apiURL,
		data,
	)

	if err != nil {
		return err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf(
			"Telegram returned status: %s",
			resp.Status,
		)
	}

	return nil
}

// ==============================
// TELEGRAM BOT POLLING
// ==============================

func startTelegramBot() {
	fmt.Println(
		"🤖 Telegram bot started",
	)

	offset := 0

	for {
		updates, err := getTelegramUpdates(
			offset,
		)

		if err != nil {
			log.Println(
				"Telegram polling error:",
				err,
			)

			time.Sleep(
				3 * time.Second,
			)

			continue
		}

		for _, update := range updates {

			// Keyingi update'dan boshlash.
			offset = update.UpdateID + 1

			// Message bo'lmasa ignore.
			if update.Message == nil {
				continue
			}

			handleTelegramMessage(
				update.Message,
			)
		}
	}
}

// ==============================
// TELEGRAM GET UPDATES
// ==============================

func getTelegramUpdates(
	offset int,
) ([]TelegramUpdate, error) {

	token := os.Getenv(
		"TELEGRAM_BOT_TOKEN",
	)

	apiURL :=
		"https://api.telegram.org/bot" +
			token +
			"/getUpdates"

	params := url.Values{}

	// Long polling.
	params.Set(
		"timeout",
		"30",
	)

	params.Set(
		"offset",
		fmt.Sprintf(
			"%d",
			offset,
		),
	)

	client := &http.Client{
		Timeout: 35 * time.Second,
	}

	resp, err := client.Get(
		apiURL + "?" + params.Encode(),
	)

	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf(
			"Telegram returned status: %s",
			resp.Status,
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

// ==============================
// TELEGRAM MESSAGE HANDLER
// ==============================

func handleTelegramMessage(
	message *TelegramMessage,
) {

	text := strings.TrimSpace(
		message.Text,
	)

	switch text {

	case "/start":
		handleStartCommand(
			message,
		)

	case "/projects":
		handleProjectsCommand(
			message,
		)

	default:
		// Keyingi bosqichlarda
		// /help va boshqa commandlar qo'shiladi.
	}
}

// ==============================
// /START
// ==============================

func handleStartCommand(
	message *TelegramMessage,
) {

	chatID := fmt.Sprintf(
		"%d",
		message.Chat.ID,
	)

	startMessage :=
		"🚀 <b>GitPulse</b>\n\n" +
			"GitHub repository monitoring bot.\n\n" +
			"📡 GitPulse GitHub'dagi push va commit'larni kuzatadi " +
			"va Telegram orqali real-time notification yuboradi.\n\n" +
			"📦 <b>Current features</b>\n" +
			"• GitHub Webhook\n" +
			"• Multi-project\n" +
			"• Webhook Security\n" +
			"• Commit notifications\n\n" +
			"🛠 <b>Commands</b>\n" +
			"/projects — projectlar\n" +
			"/help — yordam"

	if err := sendTelegramMessageToChat(
		chatID,
		startMessage,
	); err != nil {

		log.Println(
			"Telegram /start error:",
			err,
		)
	}
}

// ==============================
// /PROJECTS
// ==============================

func handleProjectsCommand(
	message *TelegramMessage,
) {

	chatID := fmt.Sprintf(
		"%d",
		message.Chat.ID,
	)

	// Read lock.
	projectsMu.RLock()

	projectList := make(
		[]string,
		0,
		len(projects),
	)

	for project := range projects {
		projectList = append(
			projectList,
			project,
		)
	}

	projectsMu.RUnlock()

	// ==============================
	// NO PROJECTS
	// ==============================

	if len(projectList) == 0 {

		text :=
			"🚀 <b>GitPulse</b>\n\n" +
				"📦 <b>Your Projects</b>\n\n" +
				"No projects registered yet.\n\n" +
				"Make a GitHub push to register a project."

		if err := sendTelegramMessageToChat(
			chatID,
			text,
		); err != nil {

			log.Println(
				"Telegram /projects error:",
				err,
			)
		}

		return
	}

	// ==============================
	// SORT
	// ==============================

	sort.Strings(
		projectList,
	)

	// ==============================
	// BUILD MESSAGE
	// ==============================

	var builder strings.Builder

	builder.WriteString(
		"🚀 <b>GitPulse</b>\n\n",
	)

	builder.WriteString(
		"📦 <b>Your Projects</b>\n\n",
	)

	for _, project := range projectList {

		project = html.EscapeString(
			project,
		)

		builder.WriteString(
			"🟢 ",
		)

		builder.WriteString(
			project,
		)

		builder.WriteString(
			"\n",
		)
	}

	// ==============================
	// TOTAL
	// ==============================

	builder.WriteString(
		"\n📊 <b>Total:</b> ",
	)

	if len(projectList) == 1 {
		builder.WriteString(
			"1 project",
		)
	} else {
		builder.WriteString(
			fmt.Sprintf(
				"%d projects",
				len(projectList),
			),
		)
	}

	// ==============================
	// SEND
	// ==============================

	if err := sendTelegramMessageToChat(
		chatID,
		builder.String(),
	); err != nil {

		log.Println(
			"Telegram /projects error:",
			err,
		)
	}
}