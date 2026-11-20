package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log"
	"strconv"
	"strings"
	"time"
)

// ============================================================
// KANAL REGISTRI
// ============================================================
//
// Har bir Telegram kanali GitPulse'da bitta kalit bilan ro'yxatdan o'tadi.
// Kalit bir vaqtning o'zida ikkita ishni bajaradi:
//
//	1. GitHub webhook imzosining siri (HMAC-SHA256)
//	2. push qaysi kanalga tushishini aniqlaydi
//
// Shuning uchun yangi loyihani ulash uchun bitta satr yetarli: o'sha kalit.
// Oldingi sxemada hamma loyiha bitta umumiy sirni baham ko'rardi va xabar
// qaysi kanalga ketishi payload ichidagi repository nomiga tayanardi — ya'ni
// sirni bilgan har kim istalgan kanalga yozishi mumkin edi.
//
// Kalit kanalga YOZILMAYDI: bot kanalga admin qilinganda kalit egasining
// shaxsiy chatiga yuboriladi. Kanal ochiq bo'lishi mumkin, sir esa sir
// bo'lib qolishi kerak.

type Channel struct {
	ChatID    string
	Title     string
	Key       string
	CreatedAt time.Time
}

func initChannelsSchema() error {

	if _, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS channels (
		chat_id    TEXT PRIMARY KEY,
		title      TEXT NOT NULL DEFAULT '',
		key        TEXT NOT NULL UNIQUE,
		created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	`); err != nil {
		return err
	}

	// project_key — loyihani kanalga bog'laydigan kalit: "owner/repo" yoki
	// shunchaki "repo", ikkalasi ham kichik harfda.
	if _, err := db.Exec(`
	CREATE TABLE IF NOT EXISTS project_channels (
		project_key TEXT PRIMARY KEY,
		chat_id     TEXT NOT NULL REFERENCES channels(chat_id) ON DELETE CASCADE,
		updated_at  TIMESTAMPTZ NOT NULL DEFAULT NOW()
	);
	`); err != nil {
		return err
	}

	return nil
}

// ============================================================
// KALIT
// ============================================================

func newChannelKey() (string, error) {

	buf := make([]byte, 16)

	if _, err := rand.Read(buf); err != nil {
		return "", err
	}

	return "gp_" + hex.EncodeToString(buf), nil
}

// ============================================================
// KANALLAR
// ============================================================

// ensureChannel kanalni ro'yxatga oladi va kalitini qaytaradi. Ikkinchi
// qaytarilgan qiymat — kanal aynan shu chaqiruvda yaratilganini bildiradi.
func ensureChannel(
	chatID int64,
	title string,
) (Channel, bool, error) {

	id := strconv.FormatInt(chatID, 10)

	var channel Channel

	err := db.QueryRow(
		`SELECT chat_id, title, key FROM channels WHERE chat_id = $1`,
		id,
	).Scan(&channel.ChatID, &channel.Title, &channel.Key)

	if err == nil {

		if title != "" && title != channel.Title {

			if _, err := db.Exec(
				`UPDATE channels SET title = $2 WHERE chat_id = $1`,
				id,
				title,
			); err != nil {
				return channel, false, err
			}

			channel.Title = title
		}

		return channel, false, nil
	}

	if !errors.Is(err, sql.ErrNoRows) {
		return Channel{}, false, err
	}

	key, err := newChannelKey()

	if err != nil {
		return Channel{}, false, err
	}

	if _, err := db.Exec(
		`INSERT INTO channels (chat_id, title, key) VALUES ($1, $2, $3)`,
		id,
		title,
		key,
	); err != nil {
		return Channel{}, false, err
	}

	return Channel{
		ChatID: id,
		Title:  title,
		Key:    key,
	}, true, nil
}

func channelByKey(key string) (Channel, error) {

	var channel Channel

	err := db.QueryRow(
		`SELECT chat_id, title, key FROM channels WHERE key = $1`,
		key,
	).Scan(&channel.ChatID, &channel.Title, &channel.Key)

	return channel, err
}

func listChannels() ([]Channel, error) {

	rows, err := db.Query(
		`SELECT chat_id, title, key, created_at FROM channels ORDER BY created_at ASC`,
	)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var channels []Channel

	for rows.Next() {

		var channel Channel

		if err := rows.Scan(
			&channel.ChatID,
			&channel.Title,
			&channel.Key,
			&channel.CreatedAt,
		); err != nil {
			return nil, err
		}

		channels = append(channels, channel)
	}

	return channels, rows.Err()
}

// ============================================================
// LOYIHA ↔ KANAL
// ============================================================

func bindProject(
	projectKey string,
	chatID string,
) error {

	projectKey = strings.ToLower(strings.TrimSpace(projectKey))

	if projectKey == "" {
		return errors.New("loyiha nomi bo'sh")
	}

	_, err := db.Exec(
		`INSERT INTO project_channels (project_key, chat_id)
		 VALUES ($1, $2)
		 ON CONFLICT (project_key)
		 DO UPDATE SET chat_id = EXCLUDED.chat_id, updated_at = NOW()`,
		projectKey,
		chatID,
	)

	return err
}

func unbindProject(projectKey string) (bool, error) {

	result, err := db.Exec(
		`DELETE FROM project_channels WHERE project_key = $1`,
		strings.ToLower(strings.TrimSpace(projectKey)),
	)

	if err != nil {
		return false, err
	}

	affected, err := result.RowsAffected()

	return affected > 0, err
}

// projectBindings bitta kanalga bog'langan loyihalar ro'yxatini qaytaradi.
func projectBindings(chatID string) ([]string, error) {

	rows, err := db.Query(
		`SELECT project_key FROM project_channels WHERE chat_id = $1 ORDER BY project_key`,
		chatID,
	)

	if err != nil {
		return nil, err
	}

	defer rows.Close()

	var keys []string

	for rows.Next() {

		var key string

		if err := rows.Scan(&key); err != nil {
			return nil, err
		}

		keys = append(keys, key)
	}

	return keys, rows.Err()
}

// channelForProject loyiha uchun bog'langan kanalni topadi.
//
// Avval to'liq nom ("samandar-hodiev/uyjoy"), keyin qisqa nom ("uyjoy")
// bo'yicha qidiriladi: to'liq nom aniqroq, chunki turli egadagi bir xil nomli
// repo'lar to'qnashmaydi.
func channelForProject(
	fullName string,
	name string,
) (Channel, bool, error) {

	var candidates []string

	if fullName != "" {
		candidates = append(candidates, strings.ToLower(fullName))
	}

	if name != "" {
		candidates = append(candidates, strings.ToLower(name))
	}

	for _, candidate := range candidates {

		var channel Channel

		err := db.QueryRow(
			`SELECT c.chat_id, c.title, c.key
			 FROM project_channels p
			 JOIN channels c ON c.chat_id = p.chat_id
			 WHERE p.project_key = $1`,
			candidate,
		).Scan(&channel.ChatID, &channel.Title, &channel.Key)

		if err == nil {
			return channel, true, nil
		}

		if !errors.Is(err, sql.ErrNoRows) {
			return Channel{}, false, err
		}
	}

	return Channel{}, false, nil
}

// ============================================================
// BOT KANALGA ADMIN QILINGANDA
// ============================================================

// handleMyChatMember bot biror chatga qo'shilgani yoki admin qilinganini
// qayta ishlaydi. Admin bo'lgan kanal darhol ro'yxatga olinadi va kalit uni
// admin qilgan odamning shaxsiy chatiga yuboriladi.
func handleMyChatMember(update *ChatMemberUpdated) {

	if update == nil {
		return
	}

	status := update.NewChatMember.Status

	chatType := update.Chat.Type

	if chatType != "channel" &&
		chatType != "supergroup" &&
		chatType != "group" {

		return
	}

	log.Println(
		"👥 Bot holati o'zgardi:",
		update.Chat.Title,
		"→",
		status,
	)

	if status != "administrator" {
		return
	}

	channel, created, err := ensureChannel(
		update.Chat.ID,
		update.Chat.Title,
	)

	if err != nil {
		log.Println("❌ Kanalni ro'yxatga olish xatosi:", err)
		return
	}

	if !created {
		return
	}

	log.Println(
		"📣 Yangi kanal ulandi:",
		channel.Title,
		channel.ChatID,
	)

	target := config.TelegramChatID

	if update.From.ID != 0 {
		target = strconv.FormatInt(update.From.ID, 10)
	}

	if err := sendTelegramMessageToChat(
		target,
		channelConnectedText(channel),
		true,
	); err != nil {

		log.Println("❌ Kalitni yuborib bo'lmadi:", err)

		// Kalit egasiga yetmasa ham, u /channels orqali ko'ra oladi.
		if target != config.TelegramChatID {

			_ = sendTelegramMessageToChat(
				config.TelegramChatID,
				channelConnectedText(channel),
				true,
			)
		}
	}
}

// handleChannelPost kanal ichidan kelgan /connect buyrug'ini qayta ishlaydi.
//
// Bu my_chat_member uchun zaxira yo'l: agar bot kanalga GitPulse ishlamay
// turgan paytda qo'shilgan bo'lsa va Telegram o'sha yangilanishni allaqachon
// o'chirgan bo'lsa, kanalda /connect yozish kifoya.
//
// Javob kanalga emas, egasining shaxsiy chatiga boradi — kalit ochiq kanalda
// ko'rinib qolmasligi kerak.
func handleChannelPost(message *TelegramMessage) {

	if message == nil {
		return
	}

	command := strings.ToLower(strings.TrimSpace(message.Text))

	command = strings.SplitN(command, "@", 2)[0]

	if command != "/connect" && command != "/start" {
		return
	}

	channel, _, err := ensureChannel(
		message.Chat.ID,
		message.Chat.Title,
	)

	if err != nil {
		log.Println("❌ Kanalni ro'yxatga olish xatosi:", err)
		return
	}

	log.Println(
		"📣 /connect kanaldan:",
		channel.Title,
		channel.ChatID,
	)

	if err := sendTelegramMessageToChat(
		config.TelegramChatID,
		channelConnectedText(channel),
		true,
	); err != nil {
		log.Println("❌ Kalitni yuborib bo'lmadi:", err)
	}
}

// ============================================================
// MATNLAR
// ============================================================

func channelConnectedText(channel Channel) string {

	title := html.EscapeString(channel.Title)

	if title == "" {
		title = "Kanal"
	}

	return fmt.Sprintf(
		"✅ <b>Kanal ulandi</b>\n\n"+
			"📣 <b>%s</b>\n"+
			"🆔 <code>%s</code>\n"+
			"🔑 <code>%s</code>\n\n"+
			"<b>GitHub webhook</b> (Settings → Webhooks → Add webhook):\n"+
			"Payload URL: <code>%s/webhook/github/%s</code>\n"+
			"Content type: <code>application/json</code>\n"+
			"Secret: <code>%s</code>\n"+
			"Events: Just the push event\n\n"+
			"<b>Lokal hook</b> (loyiha papkasida):\n"+
			"<code>%s/scripts/install-hook.sh %s</code>\n\n"+
			"Loyihani qo'lda bog'lash: <code>/bind &lt;loyiha&gt; %s</code>",

		title,
		html.EscapeString(channel.ChatID),
		html.EscapeString(channel.Key),
		html.EscapeString(strings.TrimSuffix(config.PublicBaseURL, "/")),
		html.EscapeString(channel.Key),
		html.EscapeString(channel.Key),
		html.EscapeString(config.ScriptsDir()),
		html.EscapeString(channel.Key),
		html.EscapeString(channel.ChatID),
	)
}

// ============================================================
// /channels
// ============================================================

func handleChannelsCommand(message *TelegramMessage) {

	chatID := strconv.FormatInt(message.Chat.ID, 10)

	channels, err := listChannels()

	if err != nil {

		log.Println("❌ /channels xatosi:", err)

		_ = sendTelegramMessageToChat(
			chatID,
			"❌ Baza xatosi:\n<code>"+
				html.EscapeString(err.Error())+
				"</code>",
			true,
		)

		return
	}

	if len(channels) == 0 {

		_ = sendTelegramMessageToChat(
			chatID,
			"📣 <b>Kanallar</b>\n\n"+
				"Hali bitta ham kanal ulanmagan.\n\n"+
				"Botni kanalga admin qiling — kalit shu yerga keladi. "+
				"Agar bot allaqachon admin bo'lsa, kanalda <code>/connect</code> yozing.",
			true,
		)

		return
	}

	var builder strings.Builder

	builder.WriteString("📣 <b>Kanallar</b>\n\n")

	for index, channel := range channels {

		title := html.EscapeString(channel.Title)

		if title == "" {
			title = "(nomsiz)"
		}

		builder.WriteString(
			fmt.Sprintf(
				"<b>%d. %s</b>\n🆔 <code>%s</code>\n🔑 <code>%s</code>\n",
				index+1,
				title,
				html.EscapeString(channel.ChatID),
				html.EscapeString(channel.Key),
			),
		)

		bindings, err := projectBindings(channel.ChatID)

		if err != nil {
			log.Println("❌ Bog'lanishlarni o'qish xatosi:", err)
		}

		if len(bindings) == 0 {
			builder.WriteString("📦 — bog'lanmagan\n")
		} else {
			builder.WriteString(
				"📦 " +
					html.EscapeString(strings.Join(bindings, ", ")) +
					"\n",
			)
		}

		builder.WriteString("\n")
	}

	builder.WriteString(
		"Bog'lash: <code>/bind &lt;loyiha&gt; &lt;raqam&gt;</code>\n" +
			"Uzish: <code>/unbind &lt;loyiha&gt;</code>",
	)

	_ = sendTelegramMessageToChat(
		chatID,
		builder.String(),
		true,
	)
}

// ============================================================
// /bind va /unbind
// ============================================================

func handleBindCommand(
	message *TelegramMessage,
	args []string,
) {

	chatID := strconv.FormatInt(message.Chat.ID, 10)

	if len(args) < 2 {

		_ = sendTelegramMessageToChat(
			chatID,
			"Foydalanish: <code>/bind &lt;loyiha&gt; &lt;raqam yoki chat id&gt;</code>\n\n"+
				"Raqamlarni <code>/channels</code> ko'rsatadi.",
			true,
		)

		return
	}

	project := args[0]
	target := args[1]

	channels, err := listChannels()

	if err != nil {

		_ = sendTelegramMessageToChat(
			chatID,
			"❌ Baza xatosi:\n<code>"+
				html.EscapeString(err.Error())+
				"</code>",
			true,
		)

		return
	}

	var selected *Channel

	if index, err := strconv.Atoi(target); err == nil &&
		index >= 1 &&
		index <= len(channels) {

		selected = &channels[index-1]

	} else {

		for i := range channels {
			if channels[i].ChatID == target {
				selected = &channels[i]
				break
			}
		}
	}

	if selected == nil {

		_ = sendTelegramMessageToChat(
			chatID,
			"❌ Bunday kanal topilmadi. <code>/channels</code> ni ko'ring.",
			true,
		)

		return
	}

	if err := bindProject(project, selected.ChatID); err != nil {

		_ = sendTelegramMessageToChat(
			chatID,
			"❌ Bog'lab bo'lmadi:\n<code>"+
				html.EscapeString(err.Error())+
				"</code>",
			true,
		)

		return
	}

	title := html.EscapeString(selected.Title)

	if title == "" {
		title = html.EscapeString(selected.ChatID)
	}

	_ = sendTelegramMessageToChat(
		chatID,
		fmt.Sprintf(
			"✅ <b>%s</b> → <b>%s</b>\n\n"+
				"Endi shu loyihadagi push'lar faqat shu kanalga tushadi.",
			html.EscapeString(strings.ToLower(project)),
			title,
		),
		true,
	)
}

func handleUnbindCommand(
	message *TelegramMessage,
	args []string,
) {

	chatID := strconv.FormatInt(message.Chat.ID, 10)

	if len(args) < 1 {

		_ = sendTelegramMessageToChat(
			chatID,
			"Foydalanish: <code>/unbind &lt;loyiha&gt;</code>",
			true,
		)

		return
	}

	removed, err := unbindProject(args[0])

	if err != nil {

		_ = sendTelegramMessageToChat(
			chatID,
			"❌ Baza xatosi:\n<code>"+
				html.EscapeString(err.Error())+
				"</code>",
			true,
		)

		return
	}

	if !removed {

		_ = sendTelegramMessageToChat(
			chatID,
			"Bunday bog'lanish yo'q edi.",
			true,
		)

		return
	}

	_ = sendTelegramMessageToChat(
		chatID,
		"✅ Bog'lanish uzildi: <b>"+
			html.EscapeString(strings.ToLower(args[0]))+
			"</b>",
		true,
	)
}
