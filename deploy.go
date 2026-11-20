package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"log"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// ============================================================
// DEPLOY
// ============================================================
//
// Har bir push'dan keyin deploy qilinmaydi — o'n-yigirma push'dan keyin
// bitta deploy qilinadi. Shuning uchun deploy xabari push xabaridan
// butunlay boshqacha ko'rinadi va ichida o'sha deployga kirgan barcha
// o'zgarishlar sanab o'tiladi: qaysi commit, qaysi soatda.
//
// Buning uchun har bir push bazaga yoziladi va deploy paytida hali
// deploy qilinmagan yozuvlar belgilanadi. Ya'ni "nima deploy qilindi"
// degan savolga javob taxmindan emas, yozuvdan chiqadi.

type PushRecord struct {
	SHA         string
	Branch      string
	Subject     string
	Author      string
	URL         string
	CommittedAt time.Time

	Added    int
	Modified int
	Removed  int
}

func (r PushRecord) files() int {
	return r.Added + r.Modified + r.Removed
}

func initDeploySchema() error {

	if _, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS pushes (
		id           BIGSERIAL PRIMARY KEY,
		project_key  TEXT NOT NULL,
		sha          TEXT NOT NULL,
		branch       TEXT NOT NULL DEFAULT '',
		subject      TEXT NOT NULL DEFAULT '',
		author       TEXT NOT NULL DEFAULT '',
		url          TEXT NOT NULL DEFAULT '',
		committed_at TIMESTAMPTZ,
		announced_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
		deployed_at  TIMESTAMPTZ,
		UNIQUE (project_key, sha)
	);
	`); err != nil {
		return err
	}

	// Deploy xabarida har bir commit "qancha fayl tegdi" bilan ko'rinadi.
	if _, err := db.Exec(`
	ALTER TABLE pushes ADD COLUMN IF NOT EXISTS added    INT NOT NULL DEFAULT 0;
	ALTER TABLE pushes ADD COLUMN IF NOT EXISTS modified INT NOT NULL DEFAULT 0;
	ALTER TABLE pushes ADD COLUMN IF NOT EXISTS removed  INT NOT NULL DEFAULT 0;
	`); err != nil {
		return err
	}

	_, err := db.Exec(`
	CREATE INDEX IF NOT EXISTS pushes_pending_idx
	ON pushes (project_key, committed_at)
	WHERE deployed_at IS NULL;
	`)

	return err
}

// recordPush push ichidagi har bir commitni yozib qo'yadi.
//
// Xato bo'lsa ham push xabari yuboriladi: yozuv — deploy ro'yxati uchun
// qulaylik, bildirishnomaning sharti emas.
func recordPush(
	projectKey string,
	branch string,
	commits []Commit,
) {

	projectKey = strings.ToLower(strings.TrimSpace(projectKey))

	if projectKey == "" {
		return
	}

	for _, commit := range commits {

		subject, _ := splitCommitMessage(commit.Message)

		var committedAt any

		if parsed, err := time.Parse(
			time.RFC3339,
			commit.Timestamp,
		); err == nil {
			committedAt = parsed
		}

		if _, err := db.Exec(
			`INSERT INTO pushes
			 (project_key, sha, branch, subject, author, url, committed_at,
			  added, modified, removed)
			 VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10)
			 ON CONFLICT (project_key, sha) DO NOTHING`,
			projectKey,
			commit.ID,
			branch,
			subject,
			commit.Author.Name,
			commit.URL,
			committedAt,
			len(commit.Added),
			len(commit.Modified),
			len(commit.Removed),
		); err != nil {

			log.Println("⚠️ Push yozuvini saqlab bo'lmadi:", err)

			return
		}
	}
}

// pendingPushes hali deploy qilinmagan push yozuvlarini qaytaradi.
func pendingPushes(
	projectKey string,
) ([]PushRecord, error) {

	rows, err := db.Query(
		`SELECT sha, branch, subject, author, url,
		        COALESCE(committed_at, announced_at),
		        added, modified, removed
		 FROM pushes
		 WHERE project_key = $1 AND deployed_at IS NULL
		 ORDER BY COALESCE(committed_at, announced_at) DESC`,
		strings.ToLower(projectKey),
	)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var records []PushRecord

	for rows.Next() {

		var record PushRecord

		if err := rows.Scan(
			&record.SHA,
			&record.Branch,
			&record.Subject,
			&record.Author,
			&record.URL,
			&record.CommittedAt,
			&record.Added,
			&record.Modified,
			&record.Removed,
		); err != nil {
			return nil, err
		}

		records = append(records, record)
	}

	return records, rows.Err()
}

func markDeployed(projectKey string) error {

	_, err := db.Exec(
		`UPDATE pushes SET deployed_at = NOW()
		 WHERE project_key = $1 AND deployed_at IS NULL`,
		strings.ToLower(projectKey),
	)

	return err
}

// ============================================================
// DEPLOY WEBHOOK
// ============================================================

type DeployPayload struct {
	Project  string `json:"project"`
	FullName string `json:"full_name"`

	// Environment — "prod", "staging" kabi. Ixtiyoriy.
	Environment string `json:"environment"`

	// URL — deploy qilingan sayt manzili ("https://uyjoy24.uz").
	URL string `json:"url"`

	// Commit — deploy qilingan versiya (to'liq yoki qisqa SHA).
	Commit string `json:"commit"`

	// ImageTag — serverdagi image tegi, odatda qisqa SHA.
	ImageTag string `json:"image_tag"`

	// RepoURL — commit havolalarini yasash uchun.
	RepoURL string `json:"repo_url"`

	// Status — "success" (standart) yoki "failed".
	Status string `json:"status"`
}

func deployWebhook(
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
	// KALIT VA IMZO
	// --------------------------------------------------------

	key := deployKey(r.URL.Path)

	if key == "" {
		key = strings.TrimSpace(r.Header.Get("X-GitPulse-Key"))
	}

	if key == "" {

		http.Error(
			w,
			"Channel key required",
			http.StatusUnauthorized,
		)

		return
	}

	channel, err := channelByKey(key)

	if err != nil {

		log.Println("❌ Noma'lum kanal kaliti (deploy)")

		http.Error(
			w,
			"Unknown channel key",
			http.StatusUnauthorized,
		)

		return
	}

	if !verifyGitHubSignature(
		body,
		r.Header.Get("X-Hub-Signature-256"),
		key,
	) {

		log.Println("❌ Deploy imzosi noto'g'ri")

		http.Error(
			w,
			"Invalid webhook signature",
			http.StatusUnauthorized,
		)

		return
	}

	// --------------------------------------------------------
	// PAYLOAD
	// --------------------------------------------------------

	var payload DeployPayload

	if err := json.Unmarshal(body, &payload); err != nil {

		http.Error(
			w,
			"Invalid JSON",
			http.StatusBadRequest,
		)

		return
	}

	project := strings.TrimSpace(payload.Project)

	if project == "" {

		http.Error(
			w,
			"Project name missing",
			http.StatusBadRequest,
		)

		return
	}

	projectKey := strings.ToLower(
		strings.TrimSpace(payload.FullName),
	)

	if projectKey == "" {
		projectKey = strings.ToLower(project)
	}

	// --------------------------------------------------------
	// DUBLIKAT
	// --------------------------------------------------------

	dedupKey :=
		"deploy@" +
			channel.ChatID +
			"@" + projectKey +
			"@" + payload.Commit

	if !reserveAnnouncement(dedupKey) {

		w.WriteHeader(http.StatusOK)

		_, _ = w.Write([]byte("Duplicate ignored"))

		return
	}

	// --------------------------------------------------------
	// DEPLOYGA KIRGAN O'ZGARISHLAR
	// --------------------------------------------------------

	records, err := pendingPushes(projectKey)

	if err != nil {

		log.Println("⚠️ Deploy ro'yxatini o'qib bo'lmadi:", err)

		records = nil
	}

	message := formatDeployMessage(
		project,
		payload,
		records,
	)

	log.Println("🚀 Deploy:", project, "→", channel.ChatID)

	messageID, err := sendRoutedWithID(channel, message)

	if err != nil {

		releaseAnnouncement(dedupKey)

		log.Println("❌ Deploy xabari yuborilmadi:", err)

		http.Error(
			w,
			"Failed to send Telegram message",
			http.StatusInternalServerError,
		)

		return
	}

	// Faqat deploy xabarlari pin qilinadi: kanalni ochgan odam oxirgi
	// qaysi versiya tirik ekanini tepada ko'rsin.
	if err := pinMessage(channel.ChatID, messageID); err != nil {
		log.Println("⚠️ Deployni pin qilib bo'lmadi:", err)
	}

	if err := markDeployed(projectKey); err != nil {
		log.Println("⚠️ Deploy belgisini qo'yib bo'lmadi:", err)
	}

	log.Println("✅ Deploy xabari yuborildi va pin qilindi")

	w.WriteHeader(http.StatusOK)

	_, _ = w.Write([]byte("Deploy received"))
}

// deployKey /webhook/deploy/<kalit> yo'lidan kalitni ajratadi.
func deployKey(path string) string {

	const prefix = "/webhook/deploy/"

	if !strings.HasPrefix(path, prefix) {
		return ""
	}

	return strings.Trim(strings.TrimPrefix(path, prefix), "/")
}

// ============================================================
// DEPLOY XABARI
// ============================================================

// maxDeployCommits — deploy xabarida to'liq yoziladigan commitlar soni.
const maxDeployCommits = 12

// deployQuoteBudget — commitlar ro'yxatining belgilardagi chegarasi.
// Telegram'ning 4096 belgisidan sarlavha, manzil va vaqt uchun joy
// qolishi kerak.
const deployQuoteBudget = 3000

// commitEntry deployga kirgan bitta commitni uch qatorda yozadi:
// qaysi commit va qachon, nima qilingan, qancha fayl tegilgan.
func commitEntry(record PushRecord) string {

	var builder strings.Builder

	builder.WriteString(
		"🔖 <code>" +
			html.EscapeString(shortSHA(record.SHA)) +
			"</code>",
	)

	if record.Branch != "" {
		builder.WriteString(
			" · 🌿 " + html.EscapeString(record.Branch),
		)
	}

	if !record.CommittedAt.IsZero() {
		builder.WriteString(
			" · 🕐 " +
				html.EscapeString(
					localTime(record.CommittedAt).Format("02.01.2006 15:04"),
				),
		)
	}

	builder.WriteString("\n")

	subject := record.Subject

	if subject == "" {
		subject = "(commit xabari yo'q)"
	}

	builder.WriteString(
		"<b>" +
			html.EscapeString(truncateRunes(subject, 120)) +
			"</b>\n",
	)

	var details []string

	if record.Author != "" {
		details = append(
			details,
			"👤 "+html.EscapeString(record.Author),
		)
	}

	if record.files() > 0 {
		details = append(
			details,
			fmt.Sprintf(
				"🟢 %d · 🟡 %d · 🔴 %d — %d fayl",
				record.Added,
				record.Modified,
				record.Removed,
				record.files(),
			),
		)
	}

	if len(details) > 0 {
		builder.WriteString(strings.Join(details, "  ·  ") + "\n")
	}

	return builder.String()
}

func formatDeployMessage(
	project string,
	payload DeployPayload,
	records []PushRecord,
) string {

	failed := strings.EqualFold(payload.Status, "failed")

	header := "🟢🚀 <b>DEPLOY</b>"

	if failed {
		header = "🔴🚀 <b>DEPLOY — XATO</b>"
	}

	var builder strings.Builder

	builder.WriteString(header)
	builder.WriteString("\n━━━━━━━━━━━━━━━\n\n")

	builder.WriteString(
		"📦 <b>" + html.EscapeString(project) + "</b>",
	)

	if payload.Environment != "" {
		builder.WriteString(
			" · <code>" +
				html.EscapeString(payload.Environment) +
				"</code>",
		)
	}

	builder.WriteString("\n")

	if payload.URL != "" {
		builder.WriteString(
			"🌐 " + html.EscapeString(payload.URL) + "\n",
		)
	}

	version := payload.ImageTag

	if version == "" {
		version = shortSHA(payload.Commit)
	}

	if version != "" {
		builder.WriteString(
			"🏷 <code>" + html.EscapeString(version) + "</code>\n",
		)
	}

	builder.WriteString("\n")

	// --------------------------------------------------------
	// Deployga kirgan o'zgarishlar
	// --------------------------------------------------------

	if len(records) == 0 {

		builder.WriteString(
			"📭 Oxirgi deploydan beri yangi commit yo'q\n",
		)

	} else {

		newest := records[0].CommittedAt
		oldest := records[len(records)-1].CommittedAt

		builder.WriteString(
			fmt.Sprintf(
				"📚 <b>Shu deployga kirgan %d ta o'zgarish</b>\n",
				len(records),
			),
		)

		builder.WriteString(
			"🕐 " +
				html.EscapeString(formatRange(oldest, newest)) +
				"\n\n",
		)

		shown := records

		hidden := 0

		if len(shown) > maxDeployCommits {
			hidden = len(shown) - maxDeployCommits
			shown = shown[:maxDeployCommits]
		}

		// Quote yig'ilmagan: deploy kamdan-kam bo'ladi va ochganda nima
		// chiqqani darhol ko'rinishi kerak.
		builder.WriteString("<blockquote>")

		for index, record := range shown {

			if index > 0 {
				builder.WriteString("\n")
			}

			builder.WriteString(commitEntry(record))

			// Telegram 4096 belgidan uzun xabarni qabul qilmaydi.
			// Chegaraga yaqinlashsak, qolganini raqam bilan aytamiz.
			if builder.Len() > deployQuoteBudget {

				hidden += len(shown) - index - 1

				break
			}
		}

		if hidden > 0 {
			builder.WriteString(
				fmt.Sprintf("\n… va yana %d ta commit\n", hidden),
			)
		}

		builder.WriteString("</blockquote>\n")
	}

	builder.WriteString(
		"\n🕐 Deploy: " +
			html.EscapeString(localTime(time.Now()).Format("02.01.2006 15:04")) +
			"\n",
	)

	// --------------------------------------------------------
	// Havola
	// --------------------------------------------------------

	repoURL := strings.TrimSuffix(payload.RepoURL, "/")

	if repoURL != "" && payload.Commit != "" {

		builder.WriteString(
			fmt.Sprintf(
				"\n🔗 <a href=\"%s/commit/%s\">Deploy qilingan versiya</a>",
				html.EscapeString(repoURL),
				html.EscapeString(payload.Commit),
			),
		)
	}

	return builder.String()
}

// formatRange ikkita vaqt oralig'ini qisqa yozadi.
func formatRange(from time.Time, to time.Time) string {

	if from.IsZero() || to.IsZero() {
		return "—"
	}

	fromLocal := localTime(from)
	toLocal := localTime(to)

	if fromLocal.Format("02.01.2006") == toLocal.Format("02.01.2006") {

		return fromLocal.Format("02.01.2006 15:04") +
			" → " +
			toLocal.Format("15:04")
	}

	return fromLocal.Format("02.01.2006 15:04") +
		" → " +
		toLocal.Format("02.01.2006 15:04")
}

func shortSHA(sha string) string {

	if len(sha) > 7 {
		return sha[:7]
	}

	return sha
}

// ============================================================
// TELEGRAM: pin
// ============================================================

// sendRoutedWithID xabarni yuboradi va uning message_id sini qaytaradi.
func sendRoutedWithID(
	channel Channel,
	message string,
) (int64, error) {

	data := url.Values{}

	data.Set("chat_id", channel.ChatID)
	data.Set("text", message)
	data.Set("parse_mode", "HTML")
	data.Set("disable_web_page_preview", "true")

	var sent struct {
		MessageID int64 `json:"message_id"`
	}

	if err := telegramCall("sendMessage", data, &sent); err != nil {
		return 0, err
	}

	return sent.MessageID, nil
}

func pinMessage(
	chatID string,
	messageID int64,
) error {

	if messageID == 0 {
		return errors.New("message_id yo'q")
	}

	data := url.Values{}

	data.Set("chat_id", chatID)
	data.Set("message_id", strconv.FormatInt(messageID, 10))

	// Pin xizmat xabari kanalni bezovta qilmasin.
	data.Set("disable_notification", "true")

	return telegramCall("pinChatMessage", data, nil)
}
