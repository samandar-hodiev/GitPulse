package main

import (
	"encoding/json"
	"fmt"
	"html"
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
	if err := godotenv.Load(); err != nil {
		log.Println("Warning: .env file not found")
	}

	http.HandleFunc("/health", health)
	http.HandleFunc("/webhook/github", githubWebhook)

	fmt.Println("⚡ GitPulse server started on :8080")

	log.Fatal(http.ListenAndServe(":8080", nil))
}

func health(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	w.Write([]byte("GitPulse is running"))
}

func githubWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
		return
	}

	event := r.Header.Get("X-GitHub-Event")

	fmt.Println("=== GitHub Webhook ===")
	fmt.Println("Event:", event)

	// GitHub sends a "ping" event when webhook is created.
	// We don't send Telegram notification for ping.
	if event == "ping" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Ping received"))
		return
	}

	// GitPulse processes only push events.
	if event != "push" {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("Event ignored"))
		return
	}

	var payload GitHubPayload

	if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
		http.Error(w, "Invalid JSON", http.StatusBadRequest)
		return
	}

	if payload.HeadCommit == nil {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("No head commit"))
		return
	}

	project := payload.Repository.Name

	branch := strings.TrimPrefix(
		payload.Ref,
		"refs/heads/",
	)

	commit := *payload.HeadCommit

	added := len(commit.Added)
	modified := len(commit.Modified)
	removed := len(commit.Removed)

	totalFiles := added + modified + removed

	message := formatTelegramMessage(
		project,
		branch,
		commit,
		totalFiles,
		added,
		modified,
		removed,
	)

	fmt.Println("Project:", project)
	fmt.Println("Branch:", branch)
	fmt.Println("Author:", commit.Author.Name)
	fmt.Println("Commit:", commit.Message)
	fmt.Println("Commit ID:", commit.ID)

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

func formatTelegramMessage(
	project string,
	branch string,
	commit Commit,
	totalFiles int,
	added int,
	modified int,
	removed int,
) string {

	project = html.EscapeString(project)
	branch = html.EscapeString(branch)

	author := strings.TrimSpace(commit.Author.Name)

	if author == "" {
		author = "Unknown"
	}

	author = html.EscapeString(author)

	commitMessage := strings.TrimSpace(commit.Message)

	if commitMessage == "" {
		commitMessage = "No commit message"
	}

	commitMessage = html.EscapeString(commitMessage)

	commitID := commit.ID

	if len(commitID) > 7 {
		commitID = commitID[:7]
	}

	commitID = html.EscapeString(commitID)

	commitURL := html.EscapeString(commit.URL)

	commitTime := commit.Timestamp

	if parsedTime, err := time.Parse(
		time.RFC3339,
		commit.Timestamp,
	); err == nil {

		loc, err := time.LoadLocation("Asia/Tashkent")

		if err == nil {
			parsedTime = parsedTime.In(loc)
		}

		commitTime = parsedTime.Format(
			"02.01.2006 15:04:05",
		)
	}

	return fmt.Sprintf(
		"🚀 <b>PUSH</b>\n\n"+
			"📦 <b>Project</b>\n"+
			"%s\n\n"+
			"👤 <b>Author</b>\n"+
			"%s\n\n"+
			"🌿 <b>Branch</b>\n"+
			"<code>%s</code>\n\n"+
			"📝 <b>Commit</b>\n"+
			"%s\n\n"+
			"🔖 <b>Commit ID</b>\n"+
			"<code>%s</code>\n\n"+
			"📊 <b>Changes</b>\n"+
			"• %d files changed\n"+
			"• 🟢 %d added\n"+
			"• 🟡 %d modified\n"+
			"• 🔴 %d removed\n\n"+
			"🕐 <b>Time</b>\n"+
			"%s\n\n"+
			"🔗 <a href=\"%s\">View commit on GitHub</a>",

		project,
		author,
		branch,
		commitMessage,
		commitID,
		totalFiles,
		added,
		modified,
		removed,
		commitTime,
		commitURL,
	)
}

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

	data := url.Values{}

	data.Set("chat_id", chatID)
	data.Set("text", message)
	data.Set("parse_mode", "HTML")

	// Disable Telegram's automatic GitHub link preview.
	data.Set("disable_web_page_preview", "true")

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