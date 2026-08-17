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
	"strings"
	"time"

	"github.com/joho/godotenv"
)

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

	// Routes
	http.HandleFunc("/health", health)
	http.HandleFunc("/webhook/github", githubWebhook)

	fmt.Println("⚡ GitPulse server started on :8080")

	log.Fatal(http.ListenAndServe(":8080", nil))
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

	// Read raw request body.
	// We need the raw body for HMAC signature verification.
	body, err := io.ReadAll(r.Body)

	if err != nil {
		http.Error(
			w,
			"Failed to read request body",
			http.StatusBadRequest,
		)

		return
	}

	// GitHub webhook signature.
	signature := r.Header.Get("X-Hub-Signature-256")

	// Verify that the request actually came
	// from a GitHub webhook with our secret.
	if !verifyGitHubSignature(body, signature) {
		log.Println("❌ Invalid GitHub webhook signature")

		http.Error(
			w,
			"Invalid webhook signature",
			http.StatusUnauthorized,
		)

		return
	}

	event := r.Header.Get("X-GitHub-Event")

	fmt.Println("=== GitHub Webhook ===")
	fmt.Println("Event:", event)

	// ==============================
	// PING EVENT
	// ==============================

	if event == "ping" {
		fmt.Println("🏓 GitHub ping received")

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Ping received"))

		return
	}

	// ==============================
	// EVENT FILTER
	// ==============================

	// GitPulse currently processes only push events.
	if event != "push" {
		fmt.Println("Event ignored:", event)

		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Event ignored"))

		return
	}

	// ==============================
	// PARSE PAYLOAD
	// ==============================

	var payload GitHubPayload

	if err := json.Unmarshal(body, &payload); err != nil {
		http.Error(
			w,
			"Invalid JSON",
			http.StatusBadRequest,
		)

		return
	}

	// GitHub may send events without a head commit.
	if payload.HeadCommit == nil {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("No head commit"))

		return
	}

	// ==============================
	// PROJECT
	// ==============================

	project := payload.Repository.Name

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
	// SERVER LOG
	// ==============================

	fmt.Println("Project:", project)
	fmt.Println("Branch:", branch)
	fmt.Println("Author:", commit.Author.Name)
	fmt.Println("Commit:", commit.Message)
	fmt.Println("Commit ID:", commit.ID)

	// ==============================
	// SEND TELEGRAM
	// ==============================

	if err := sendTelegramMessage(message); err != nil {
		log.Println("Telegram error:", err)

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
// GITHUB SIGNATURE VERIFICATION
// ==============================

func verifyGitHubSignature(
	body []byte,
	signature string,
) bool {

	secret := os.Getenv("GITHUB_WEBHOOK_SECRET")

	if secret == "" {
		return false
	}

	if signature == "" {
		return false
	}

	const prefix = "sha256="

	if !strings.HasPrefix(signature, prefix) {
		return false
	}

	receivedSignature := strings.TrimPrefix(
		signature,
		prefix,
	)

	// Create HMAC-SHA256 hash using our secret.
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
// TELEGRAM MESSAGE
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
	// ESCAPE USER/REMOTE DATA
	// ==============================

	project = html.EscapeString(project)
	branch = html.EscapeString(branch)

	// ==============================
	// AUTHOR
	// ==============================

	author := strings.TrimSpace(
		commit.Author.Name,
	)

	if author == "" {
		author = "Unknown"
	}

	author = html.EscapeString(author)

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
// TELEGRAM API
// ==============================

func sendTelegramMessage(message string) error {
	token := os.Getenv("TELEGRAM_BOT_TOKEN")
	chatID := os.Getenv("TELEGRAM_CHAT_ID")

	if token == "" {
		return fmt.Errorf(
			"TELEGRAM_BOT_TOKEN is missing",
		)
	}

	if chatID == "" {
		return fmt.Errorf(
			"TELEGRAM_CHAT_ID is missing",
		)
	}

	apiURL :=
		"https://api.telegram.org/bot" +
			token +
			"/sendMessage"

	// ==============================
	// REQUEST DATA
	// ==============================

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

	// Disable Telegram automatic link preview.
	data.Set(
		"disable_web_page_preview",
		"true",
	)

	// ==============================
	// HTTP CLIENT
	// ==============================

	client := &http.Client{
		Timeout: 10 * time.Second,
	}

	// ==============================
	// REQUEST
	// ==============================

	resp, err := client.PostForm(
		apiURL,
		data,
	)

	if err != nil {
		return err
	}

	defer resp.Body.Close()

	// ==============================
	// TELEGRAM RESPONSE
	// ==============================

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf(
			"Telegram returned status: %s",
			resp.Status,
		)
	}

	return nil
}