package main

import (
	"strings"
	"testing"
	"time"
)

const realCommitMessage = `fix(console): grafikdagi kalendar — dropdown, kartochkani cho'zmaydi

Kalendar paneli oqimda edi va ochilganda butun Dinamika kartochkasini
pastga cho'zardi. Endi u tugma ostida suzadi.

Tekshirilgan: yangi Playwright testi — panel ochilganda kartochka
balandligi o'zgarmaydi.

Co-Authored-By: Claude Opus 5.5 <noreply@anthropic.com>`

func TestSplitCommitMessageDropsTrailers(t *testing.T) {

	subject, body := splitCommitMessage(realCommitMessage)

	if subject != "fix(console): grafikdagi kalendar — dropdown, kartochkani cho'zmaydi" {
		t.Fatalf("sarlavha noto'g'ri: %q", subject)
	}

	if strings.Contains(body, "Co-Authored-By") {
		t.Error("trailer tanada qolib ketdi")
	}

	if !strings.Contains(body, "Kalendar paneli oqimda edi") {
		t.Error("tana tushib qoldi — o'zgarish izohi yo'qolmasligi kerak")
	}

	if strings.HasSuffix(body, "\n") {
		t.Error("tana oxirida ortiqcha bo'sh qator")
	}
}

func TestSplitCommitMessageLimitsLines(t *testing.T) {

	var lines []string

	lines = append(lines, "feat: uzun commit")

	for i := 0; i < 40; i++ {
		lines = append(lines, "qator")
	}

	_, body := splitCommitMessage(strings.Join(lines, "\n"))

	if got := strings.Count(body, "\n") + 1; got > maxBodyLines+1 {
		t.Errorf("tana %d qator, kutilgani ko'pi bilan %d", got, maxBodyLines+1)
	}
}

func TestChangedFilesBlockCountsAndCaps(t *testing.T) {

	var modified []string

	for i := 0; i < 20; i++ {
		modified = append(modified, "src/file.ts")
	}

	// Bir xil yo'l takrorlansa bir marta sanalsin.
	block := changedFilesBlock([]Commit{{Modified: modified}})

	if strings.Count(block, "<code>") != 1 {
		t.Errorf("takroriy fayl bir marta sanalishi kerak edi:\n%s", block)
	}

	var many []string

	for i := 0; i < maxListedFiles+5; i++ {
		many = append(many, "src/file"+string(rune('a'+i))+".ts")
	}

	block = changedFilesBlock([]Commit{{Modified: many}})

	if strings.Count(block, "<code>") != maxListedFiles {
		t.Errorf("ro'yxat %d ta bilan cheklanishi kerak edi", maxListedFiles)
	}

	if !strings.Contains(block, "+5 ta fayl") {
		t.Errorf("qolgan fayllar soni ko'rsatilmadi:\n%s", block)
	}
}

func TestFormatTelegramMessage(t *testing.T) {

	commit := Commit{
		ID:        "0c92b243123321cb9d9f8221892fa03902197908",
		Message:   realCommitMessage,
		Timestamp: "2026-11-20T17:31:03+05:00",
		URL:       "https://github.com/samandar-hodiev/uyjoy/commit/0c92b24",
		Added:     []string{"web/src/components/Calendar.tsx"},
		Modified:  []string{"web/src/app/page.tsx", "web/src/lib/routes.ts"},
	}

	commit.Author.Name = "samandar-hodiev"

	message := formatTelegramMessage(
		"uyjoy",
		"main",
		commit,
		[]Commit{commit},
		3, 1, 2, 0,
	)

	t.Log("\n" + message)

	for _, want := range []string{
		"🟡 <b>COMMIT · PUSH</b>",
		"<b>uyjoy</b>",
		"<code>main</code>",
		"<code>0c92b24</code>",
		"Kalendar paneli oqimda edi",
		"web/src/components/Calendar.tsx",
		"20.11.2026 17:31",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("xabarda %q yo'q", want)
		}
	}

	if strings.Contains(message, "Co-Authored-By") {
		t.Error("trailer xabarga tushib ketdi")
	}

	if len([]rune(message)) > 4096 {
		t.Errorf("xabar Telegram chegarasidan uzun: %d", len([]rune(message)))
	}
}

func TestFormatTelegramMessageStaysUnderTelegramLimit(t *testing.T) {

	long := strings.Repeat("juda uzun qator ", 400)

	commit := Commit{
		ID:      "abc1234567890",
		Message: "feat: uzun\n\n" + long,
	}

	var files []string

	for i := 0; i < 300; i++ {
		files = append(files, strings.Repeat("a", 120)+"/file.ts")
	}

	commit.Modified = files

	message := formatTelegramMessage(
		"loyiha",
		"main",
		commit,
		[]Commit{commit},
		300, 0, 300, 0,
	)

	if len([]rune(message)) > 4096 {
		t.Errorf("xabar Telegram chegarasidan uzun: %d", len([]rune(message)))
	}
}

func TestFormatDeployMessageListsCommits(t *testing.T) {

	committed := time.Date(2026, 11, 20, 18, 21, 0, 0, time.UTC)

	records := []PushRecord{
		{
			SHA:         "d3afbaa125e",
			Branch:      "main",
			Subject:     "feat(console): agentliklar tab",
			Author:      "samandar-hodiev",
			CommittedAt: committed,
			Added:       1,
			Modified:    19,
			Removed:     1,
		},
		{
			SHA:         "02248d13988",
			Branch:      "main",
			Subject:     "feat(console): yig'ilgan menyuda papka",
			Author:      "samandar-hodiev",
			CommittedAt: committed.Add(-time.Hour),
			Modified:    3,
		},
	}

	message := formatDeployMessage(
		"uyjoy",
		DeployPayload{
			Environment: "prod",
			URL:         "https://uyjoy24.uz",
			ImageTag:    "d3afbaa",
			Commit:      "d3afbaa125e",
			RepoURL:     "https://github.com/samandar-hodiev/uyjoy",
		},
		records,
	)

	t.Log("\n" + message)

	for _, want := range []string{
		"🟢🚀 <b>DEPLOY</b>",
		"uyjoy24.uz",
		"2 ta o'zgarish",
		"d3afbaa",
		"menyuda papka",
		"👤 samandar-hodiev",
		"🟢 1 · 🟡 19 · 🔴 1 — 21 fayl",
		"🌿 main",
		"20.11.2026",
	} {
		if !strings.Contains(message, want) {
			t.Errorf("deploy xabarida %q yo'q", want)
		}
	}

	// Quote ochiq turishi kerak, yig'ilgan emas.
	if !strings.Contains(message, "<blockquote>") {
		t.Error("quote yo'q")
	}

	if strings.Contains(message, "blockquote expandable") {
		t.Error("quote yig'ilgan holatda — deployda ochiq bo'lishi kerak")
	}

	// Push va deploy aralashmasligi kerak.
	if strings.Contains(message, "COMMIT · PUSH") {
		t.Error("deploy xabari push sarlavhasini olib qolibdi")
	}
}

func TestFormatDeployMessageStaysUnderTelegramLimit(t *testing.T) {

	var records []PushRecord

	for i := 0; i < 60; i++ {
		records = append(records, PushRecord{
			SHA:         strings.Repeat("a", 40),
			Branch:      "feature/juda-uzun-branch-nomi",
			Subject:     strings.Repeat("uzun sarlavha ", 20),
			Author:      "samandar-hodiev",
			CommittedAt: time.Now(),
			Modified:    12,
		})
	}

	message := formatDeployMessage(
		"uyjoy",
		DeployPayload{Environment: "prod", URL: "https://uyjoy24.uz"},
		records,
	)

	if len([]rune(message)) > 4096 {
		t.Errorf("deploy xabari chegaradan uzun: %d", len([]rune(message)))
	}

	if !strings.Contains(message, "va yana") {
		t.Error("ko'rsatilmagan commitlar soni yozilmadi")
	}
}

func TestFormatDeployMessageWithoutCommits(t *testing.T) {

	message := formatDeployMessage(
		"uyjoy",
		DeployPayload{Environment: "prod"},
		nil,
	)

	if !strings.Contains(message, "yangi commit yo'q") {
		t.Errorf("bo'sh deploy holati yozilmadi:\n%s", message)
	}
}
