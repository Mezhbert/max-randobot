package handlers

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// mskLoc — московская зона для CSV-выгрузок.
func (b *Bot) mskLoc() *time.Location {
	if loc, err := time.LoadLocation("Europe/Moscow"); err == nil {
		return loc
	}
	return time.FixedZone("MSK", 3*3600)
}

// showGiveawayAnalytics — аналитика конкретного розыгрыша: участники,
// динамика по дням, отток, подозрительный трафик. Данные живые (для active
// обновляются при каждом открытии/нажатии 🔄; для finished — итоги).
func (b *Bot) showGiveawayAnalytics(ctx context.Context, uid, gid int64) {
	g, err := b.db.GetGiveaway(ctx, gid, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return
	}

	stats, err := b.db.GiveawayParticipantStats(ctx, g.ID)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Ошибка загрузки аналитики.")
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("📊 Аналитика «%s»\n", g.Name))
	sb.WriteString(fmt.Sprintf("Статус: %s\n\n", statusName(g.Status)))

	// Основные метрики.
	sb.WriteString(fmt.Sprintf("👥 Участников: %d\n", stats.ParticipantsTotal))
	if g.FinishMode == "count" {
		sb.WriteString(fmt.Sprintf("🎯 Цель: %d участников\n", g.FinishParticipantsCount))
	}
	if len(g.WinnersUserIDs) > 0 {
		sb.WriteString(fmt.Sprintf("🏆 Победителей выбрано: %d\n", len(g.WinnersUserIDs)))
	}
	// Отток: отписались от каналов после участия.
	if stats.Unsubscribed > 0 {
		pct := 0
		if stats.ParticipantsTotal > 0 {
			pct = stats.Unsubscribed * 100 / stats.ParticipantsTotal
		}
		sb.WriteString(fmt.Sprintf("📉 Отписались после участия: %d (%d%%)\n", stats.Unsubscribed, pct))
	}

	// Динамика по дням (последние 7, свежие внизу).
	if len(stats.JoinsByDay) > 0 {
		days := make([]string, 0, len(stats.JoinsByDay))
		for d := range stats.JoinsByDay {
			days = append(days, d)
		}
		sort.Strings(days)
		if len(days) > 7 {
			days = days[len(days)-7:]
		}
		sb.WriteString("\n📅 Динамика (МСК, последние дни):\n")
		max := 1
		for _, d := range days {
			if stats.JoinsByDay[d] > max {
				max = stats.JoinsByDay[d]
			}
		}
		for _, d := range days {
			n := stats.JoinsByDay[d]
			bar := strings.Repeat("█", (n*10)/max)
			if bar == "" {
				bar = "▏"
			}
			sb.WriteString(fmt.Sprintf("  %s %s %d\n", d, bar, n))
		}
	}

	// Подозрительный трафик (эвристики, только пометки).
	var warnings []string
	if stats.NoUsername > 0 && stats.ParticipantsTotal > 0 {
		pct := stats.NoUsername * 100 / stats.ParticipantsTotal
		if pct >= 30 {
			warnings = append(warnings, fmt.Sprintf("⚠️ %d%% участников без username — возможны боты", pct))
		}
	}
	if stats.Bots > 0 {
		warnings = append(warnings, fmt.Sprintf("⚠️ Помечены как боты: %d", stats.Bots))
	}
	if stats.FreshAccounts > 0 && stats.ParticipantsTotal > 0 {
		pct := stats.FreshAccounts * 100 / stats.ParticipantsTotal
		if pct >= 20 {
			warnings = append(warnings, fmt.Sprintf("⚠️ %d%% аккаунтов младше 3 дней — возможна накрутка", pct))
		}
	}
	if len(stats.Bursts) > 0 {
		warnings = append(warnings, fmt.Sprintf("⚠️ Всплески регистраций (>5/час): %s", strings.Join(stats.Bursts, ", ")))
	}
	if len(warnings) > 0 {
		sb.WriteString("\n🤖 Подозрительный трафик:\n")
		for _, w := range warnings {
			sb.WriteString(w + "\n")
		}
	}
	// Демография (возраст/город) недоступна: Bot API MAX не отдаёт эти данные.

	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("👥 Участники", util.CBGwParticipants+fmt.Sprintf("%d", g.ID)).
		AddCallBack("🔄 Обновить", util.CBGwAnalytics+fmt.Sprintf("%d", g.ID))
	kb.AddRow().
		AddCallBack("📎 CSV участников", util.CBGwCSVParts+fmt.Sprintf("%d", g.ID)).
		AddCallBack("← К розыгрышу", util.CBGwOpen+fmt.Sprintf("%d", g.ID))

	_, _ = b.api.SendTextWithKeyboard(ctx, uid, sb.String(), kb)
}
// каждого — анти-накрутка).
func (b *Bot) showGiveawayParticipants(ctx context.Context, uid, gid int64) {
	g, err := b.db.GetGiveaway(ctx, gid, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return
	}
	if g.Status == store.StatusFinished {
		_ = b.api.SendText(ctx, uid, "Розыгрыш завершён — список участников изменить нельзя.")
		return
	}
	rows, err := b.db.ListParticipants(ctx, g.ID)
	if err != nil || len(rows) == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid, "Участников пока нет.",
			util.AnalyticsKeyboard(g.ID))
		return
	}

	var sb strings.Builder
	sb.WriteString(fmt.Sprintf("👥 Участники «%s» (%d):\n\n", g.Name, len(rows)))
	kb := model.NewKeyboard()
	winnersSet := make(map[int64]bool, len(g.WinnersUserIDs))
	for _, w := range g.WinnersUserIDs {
		winnersSet[w] = true
	}
	shown := 0
	for _, r := range rows {
		if shown >= 30 { // лимит кнопок на сообщение
			sb.WriteString(fmt.Sprintf("…и ещё %d\n", len(rows)-shown))
			break
		}
		name := r.FirstName
		if name == "" {
			name = fmt.Sprintf("Участник #%d", r.UserID)
		}
		mark := ""
		if r.IsBot {
			mark = " 🤖"
		} else if r.Username == "" {
			mark = " ⚠️"
		}
		if winnersSet[r.UserID] {
			mark = " 🏆" // победителя удалить нельзя
		}
		sb.WriteString(fmt.Sprintf("• %s%s\n", name, mark))
		if winnersSet[r.UserID] {
			shown++
			continue
		}
		kb.AddRow().AddCallBack("🗑 "+truncate(name, 24), fmt.Sprintf("%s%d:%d", util.CBGwKickPart, g.ID, r.UserID))
		shown++
	}
	kb.AddRow().AddCallBack("← К аналитике", util.CBGwAnalytics+fmt.Sprintf("%d", g.ID))
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, sb.String(), kb)
}

// kickParticipant — удаление участника (анти-накрутка). Победителя удалить нельзя.
func (b *Bot) kickParticipant(ctx context.Context, uid, gid, userID int64) {
	g, err := b.db.GetGiveaway(ctx, gid, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return
	}
	if g.Status == store.StatusFinished {
		_ = b.api.SendText(ctx, uid, "Розыгрыш завершён — участников удалить нельзя.")
		return
	}
	for _, w := range g.WinnersUserIDs {
		if w == userID {
			_ = b.api.SendText(ctx, uid, "Нельзя удалить победителя.")
			return
		}
	}
	if err := b.db.DeleteParticipant(ctx, gid, userID); err != nil {
		_ = b.api.SendText(ctx, uid, "Участник не найден.")
		return
	}
	_ = b.api.SendText(ctx, uid, "🗑 Участник удалён из розыгрыша.")
	// Обновляем счётчик в постах (стало на 1 меньше).
	b.refreshPublishedPosts(ctx, gid)
	b.showGiveawayParticipants(ctx, uid, gid)
}

// exportParticipantsCSV — CSV-выгрузка участников розыгрыша.
func (b *Bot) exportParticipantsCSV(ctx context.Context, uid, gid int64) {
	g, err := b.db.GetGiveaway(ctx, gid, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return
	}
	rows, err := b.db.ListParticipants(ctx, g.ID)
	if err != nil || len(rows) == 0 {
		_ = b.api.SendText(ctx, uid, "Участников пока нет — выгружать нечего.")
		return
	}
	var csvw strings.Builder
	csvw.WriteString("user_id,name,username,is_bot,joined_at_msk\n")
	for _, r := range rows {
		joined := r.JoinedAt.In(b.mskLoc()).Format("02.01.2006 15:04:05")
		fmt.Fprintf(&csvw, "%d,%s,%s,%t,%s\n",
			r.UserID, csvEscape(r.FirstName), csvEscape(r.Username), r.IsBot, joined)
	}
	filename := fmt.Sprintf("participants_%d.csv", g.ID)
	if err := b.api.SendFile(ctx, uid, filename, []byte(csvw.String())); err != nil {
		b.log.Warn("participants CSV upload failed", "err", err)
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"📎 CSV (файлом не удалось):\n\n"+truncate(csvw.String(), 3500), util.AnalyticsKeyboard(g.ID))
		return
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"✅ CSV с участниками отправлен выше.", util.AnalyticsKeyboard(g.ID))
}
