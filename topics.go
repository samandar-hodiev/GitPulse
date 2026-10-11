package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
)

// ============================================================
// PROJECT TOPICS
// ============================================================
//
// Every project gets its own Telegram topic, so pushes from different
// repositories never mix in one feed. Topics work in a private chat with the
// bot (BotFather → Bot Settings → Threaded Mode) and in a group with Topics
// enabled (the bot needs the "Manage Topics" admin right).
//
// The topic is created on the project's first push and remembered in
// project_topics. If the topic is deleted in Telegram, it is created again on
// the next push. When topics are not available in the chat, the message goes
// to the chat as before, prefixed with a #Project hashtag so a tap on it
// filters that project's pushes.

var (
	topicsMu sync.Mutex

	// topicsUnavailableUntil stops GitPulse from asking Telegram to create a
	// topic on every push while topics are switched off in the chat.
	topicsUnavailableUntil time.Time
)

const topicsRetryAfter = 10 * time.Minute

// Telegram only accepts these six colours for topic icons.
var topicIconColors = []int{
	7322096,  // blue
	16766590, // yellow
	13338331, // violet
	9367192,  // green
	16749490, // rose
	16478047, // red
}

func initTopicsSchema() error {

	_, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS project_topics (
		chat_id TEXT NOT NULL,
		project_key TEXT NOT NULL,
		thread_id BIGINT NOT NULL,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		PRIMARY KEY (chat_id, project_key)
	);
	`)

	return err
}

// projectKey makes "sat-platform" and "Sat-Platform" (a renamed repository)
// share one topic.
func projectKey(project string) string {
	return strings.ToLower(strings.TrimSpace(project))
}

// sendProjectNotification posts a push notification into the project's topic.
func sendProjectNotification(
	project string,
	message string,
) error {

	chatID := config.TelegramChatID

	threadID, err := projectThread(chatID, project)
	if err != nil {
		log.Println("⚠️ Topic unavailable, sending to the chat:", err)
	}

	if threadID == 0 {
		return sendToThread(chatID, 0, projectHashtag(project)+"\n"+message)
	}

	err = sendToThread(chatID, threadID, message)

	var tgErr *telegramError
	if errors.As(err, &tgErr) && tgErr.threadGone() {

		// The topic was deleted in Telegram: forget it and open a new one.
		log.Println("🧵 Topic for", project, "was deleted, creating it again")

		forgetProjectThread(chatID, project)

		threadID, err = projectThread(chatID, project)
		if err != nil || threadID == 0 {
			return sendToThread(chatID, 0, projectHashtag(project)+"\n"+message)
		}

		return sendToThread(chatID, threadID, message)
	}

	return err
}

// projectThread returns the project's topic id, creating the topic on first
// use. It returns 0 (and no error) while topics are known to be unavailable.
func projectThread(
	chatID string,
	project string,
) (int64, error) {

	topicsMu.Lock()
	defer topicsMu.Unlock()

	key := projectKey(project)

	var threadID int64

	err := db.QueryRow(
		`SELECT thread_id FROM project_topics WHERE chat_id = $1 AND project_key = $2`,
		chatID,
		key,
	).Scan(&threadID)

	if err == nil {
		return threadID, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return 0, err
	}

	if time.Now().Before(topicsUnavailableUntil) {
		return 0, nil
	}

	threadID, err = createForumTopic(chatID, project)
	if err != nil {
		topicsUnavailableUntil = time.Now().Add(topicsRetryAfter)
		return 0, err
	}

	if _, err := db.Exec(
		`INSERT INTO project_topics (chat_id, project_key, thread_id)
		 VALUES ($1, $2, $3)
		 ON CONFLICT (chat_id, project_key) DO UPDATE SET thread_id = EXCLUDED.thread_id`,
		chatID,
		key,
		threadID,
	); err != nil {
		return 0, err
	}

	log.Println("🧵 Topic created for", project, "thread", threadID)

	return threadID, nil
}

func forgetProjectThread(
	chatID string,
	project string,
) {

	topicsMu.Lock()
	defer topicsMu.Unlock()

	if _, err := db.Exec(
		`DELETE FROM project_topics WHERE chat_id = $1 AND project_key = $2`,
		chatID,
		projectKey(project),
	); err != nil {
		log.Println("❌ Could not forget topic:", err)
	}
}

// projectHashtag turns "Sat-Platform" into "#SatPlatform": Telegram hashtags
// stop at the first character that is not a letter, digit or underscore.
func projectHashtag(project string) string {

	var b strings.Builder

	for _, r := range project {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' {
			b.WriteRune(r)
		}
	}

	if b.Len() == 0 {
		return "#project"
	}

	return "#" + b.String()
}

func topicColor(project string) int {

	h := fnv.New32a()
	_, _ = h.Write([]byte(projectKey(project)))

	return topicIconColors[int(h.Sum32()%uint32(len(topicIconColors)))]
}

// ============================================================
// TELEGRAM API (topics)
// ============================================================

type telegramError struct {
	Code        int
	Description string
}

func (e *telegramError) Error() string {
	return fmt.Sprintf("Telegram %d: %s", e.Code, e.Description)
}

// threadGone reports that the topic the message was sent to no longer exists.
func (e *telegramError) threadGone() bool {
	d := strings.ToLower(e.Description)
	return strings.Contains(d, "thread not found") ||
		strings.Contains(d, "topic_deleted") ||
		strings.Contains(d, "topic not found")
}

func telegramCall(
	method string,
	data url.Values,
	result any,
) error {

	apiURL :=
		"https://api.telegram.org/bot" +
			config.TelegramBotToken +
			"/" + method

	client := &http.Client{
		Timeout: 30 * time.Second,
	}

	resp, err := client.PostForm(apiURL, data)
	if err != nil {
		return err
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}

	var envelope struct {
		OK          bool            `json:"ok"`
		ErrorCode   int             `json:"error_code"`
		Description string          `json:"description"`
		Result      json.RawMessage `json:"result"`
	}

	if err := json.Unmarshal(body, &envelope); err != nil {
		return fmt.Errorf("Telegram returned %s", resp.Status)
	}

	if !envelope.OK {
		return &telegramError{Code: envelope.ErrorCode, Description: envelope.Description}
	}

	if result != nil {
		return json.Unmarshal(envelope.Result, result)
	}

	return nil
}

func createForumTopic(
	chatID string,
	project string,
) (int64, error) {

	name := []rune(project)
	if len(name) > 128 {
		name = name[:128]
	}

	data := url.Values{}
	data.Set("chat_id", chatID)
	data.Set("name", string(name))
	data.Set("icon_color", strconv.Itoa(topicColor(project)))

	var topic struct {
		MessageThreadID int64 `json:"message_thread_id"`
	}

	if err := telegramCall("createForumTopic", data, &topic); err != nil {
		return 0, err
	}

	if topic.MessageThreadID == 0 {
		return 0, errors.New("Telegram returned no message_thread_id")
	}

	return topic.MessageThreadID, nil
}

// sendToThread sends an HTML message; threadID 0 means the chat itself.
func sendToThread(
	chatID string,
	threadID int64,
	message string,
) error {

	data := url.Values{}
	data.Set("chat_id", chatID)
	data.Set("text", message)
	data.Set("parse_mode", "HTML")
	data.Set("disable_web_page_preview", "true")

	if threadID != 0 {
		data.Set("message_thread_id", strconv.FormatInt(threadID, 10))
	}

	return telegramCall("sendMessage", data, nil)
}
