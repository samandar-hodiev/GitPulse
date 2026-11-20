package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"html"
	"log"
	"net/url"
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
	ChatType  string
	CreatedAt time.Time
}

// isGroup — guruhda bot o'z nomidan yozadi va reply keyboard ko'rinadi;
// kanalda esa xabar kanal nomidan chiqadi va tugmalar umuman bo'lmaydi.
func (c Channel) isGroup() bool {
	return c.ChatType == "group" || c.ChatType == "supergroup"
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

	if _, err := db.Exec(`
	ALTER TABLE channels
	ADD COLUMN IF NOT EXISTS chat_type TEXT NOT NULL DEFAULT '';
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
	chatType string,
) (Channel, bool, error) {

	id := strconv.FormatInt(chatID, 10)

	var channel Channel

	err := db.QueryRow(
		`SELECT chat_id, title, key, chat_type FROM channels WHERE chat_id = $1`,
		id,
	).Scan(
		&channel.ChatID,
		&channel.Title,
		&channel.Key,
		&channel.ChatType,
	)

	if err == nil {

		if (title != "" && title != channel.Title) ||
			(chatType != "" && chatType != channel.ChatType) {

			if title == "" {
				title = channel.Title
			}

			if chatType == "" {
				chatType = channel.ChatType
			}

			if _, err := db.Exec(
				`UPDATE channels SET title = $2, chat_type = $3 WHERE chat_id = $1`,
				id,
				title,
				chatType,
			); err != nil {
				return channel, false, err
			}

			channel.Title = title
			channel.ChatType = chatType
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
		`INSERT INTO channels (chat_id, title, key, chat_type) VALUES ($1, $2, $3, $4)`,
		id,
		title,
		key,
		chatType,
	); err != nil {
		return Channel{}, false, err
	}

	return Channel{
		ChatID:   id,
		Title:    title,
		Key:      key,
		ChatType: chatType,
	}, true, nil
}

func channelByKey(key string) (Channel, error) {

	var channel Channel

	err := db.QueryRow(
		`SELECT chat_id, title, key, chat_type FROM channels WHERE key = $1`,
		key,
	).Scan(
		&channel.ChatID,
		&channel.Title,
		&channel.Key,
		&channel.ChatType,
	)

	return channel, err
}

func listChannels() ([]Channel, error) {

	rows, err := db.Query(
		`SELECT chat_id, title, key, chat_type, created_at
		 FROM channels ORDER BY created_at ASC`,
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
			&channel.ChatType,
			&channel.CreatedAt,
		); err != nil {
			return nil, err
		}

		channels = append(channels, channel)
	}

	return channels, rows.Err()
}

// refreshChannelTypes turi noma'lum kanallarni Telegram'dan so'rab to'ldiradi.
//
// Kerak, chunki chat turi keyinroq qo'shilgan: undan oldin ro'yxatga olingan
// kanallarda bu maydon bo'sh qoladi, guruh esa kanaldan boshqacha ishlanadi.
func refreshChannelTypes() {

	channels, err := listChannels()

	if err != nil {
		log.Println("❌ Kanallarni o'qib bo'lmadi:", err)
		return
	}

	for _, channel := range channels {

		if channel.ChatType != "" {
			continue
		}

		data := url.Values{}
		data.Set("chat_id", channel.ChatID)

		var chat struct {
			Type  string `json:"type"`
			Title string `json:"title"`
		}

		if err := telegramCall("getChat", data, &chat); err != nil {
			log.Println("⚠️ getChat xatosi:", channel.ChatID, err)
			continue
		}

		if _, err := db.Exec(
			`UPDATE channels
			 SET chat_type = $2,
			     title = COALESCE(NULLIF($3, ''), title)
			 WHERE chat_id = $1`,
			channel.ChatID,
			chat.Type,
			chat.Title,
		); err != nil {
			log.Println("❌ Kanal turini saqlab bo'lmadi:", err)
			continue
		}

		log.Println(
			"📣 Kanal turi aniqlandi:",
			chat.Title,
			"→",
			chat.Type,
		)
	}
}

// sendRouted push xabarini kanalga (yoki guruhga) yuboradi.
//
// Guruhga yuborilganda eski reply keyboard olib tashlanadi: bot bir marta
// tugma ko'rsatgan bo'lsa, u guruhdagi hamma uchun osilib qolardi. Kanalda
// reply keyboard umuman bo'lmaydi, shuning uchun u yerga bu maydon
// yuborilmaydi.
func sendRouted(
	channel Channel,
	message string,
) error {

	data := url.Values{}

	data.Set("chat_id", channel.ChatID)
	data.Set("text", message)
	data.Set("parse_mode", "HTML")
	data.Set("disable_web_page_preview", "true")

	if channel.isGroup() {
		data.Set("reply_markup", `{"remove_keyboard":true}`)
	}

	return telegramCall("sendMessage", data, nil)
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

	// Bot chiqarib yuborilsa yozuvni ham olib tashlaymiz: aks holda
	// loyiha endi mavjud bo'lmagan chatga bog'langan bo'lib qolaveradi va
	// har push 400 bilan tugaydi. Bog'lanishlar birga o'chadi (CASCADE).
	if status == "left" || status == "kicked" {

		if _, err := db.Exec(
			`DELETE FROM channels WHERE chat_id = $1`,
			strconv.FormatInt(update.Chat.ID, 10),
		); err != nil {
			log.Println("❌ Kanal yozuvini o'chirib bo'lmadi:", err)
			return
		}

		log.Println(
			"🗑 Kanal ro'yxatdan chiqarildi:",
			update.Chat.Title,
		)

		return
	}

	if status != "administrator" {
		return
	}

	channel, created, err := ensureChannel(
		update.Chat.ID,
		update.Chat.Title,
		chatType,
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
		message.Chat.Type,
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

	// Guruhda xabar bot nomidan chiqadi va yonida "Admin" yozuvi turadi.
	// Kanalda esa xabar kanal nomidan chiqadi — odatda kerakli ko'rinish
	// shu, shuning uchun ogohlantiramiz.
	warning := ""

	if channel.isGroup() {
		warning = "\n⚠️ Bu <b>guruh</b>, kanal emas. Guruhda xabar bot nomidan " +
			"chiqadi. Xabar kanal nomidan chiqishi uchun kanal (channel) " +
			"oching va botni o'sha yerga admin qiling.\n"
	}

	return fmt.Sprintf(
		"✅ <b>Kanal ulandi</b>\n"+
			warning+
			"\n📣 <b>%s</b>\n"+
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

		kind := "kanal"

		if channel.isGroup() {
			kind = "guruh ⚠️"
		}

		builder.WriteString(
			fmt.Sprintf(
				"<b>%d. %s</b> — %s\n🆔 <code>%s</code>\n🔑 <code>%s</code>\n",
				index+1,
				title,
				kind,
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
