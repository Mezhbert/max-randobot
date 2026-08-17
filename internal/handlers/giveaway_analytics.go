package handlers

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// showGiveawayAnalytics — аналитика конкретного розыгрыша: участники,
// динамика по дням, подозрительный трафик. Данные живые (обновляются при
// каждом открытии — для active розыгрышей отображают текущее состояние).
func (b *Bot) showGiveawayAnalytics(ctx context.Context, uid, gid int64, cbID string) {
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
	if len(stats.Bursts) > 0 {
		warnings = append(warnings, fmt.Sprintf("⚠️ Всплески регистраций (>5/час): %s", strings.Join(stats.Bursts, ", ")))
	}
	if len(warnings) > 0 {
		sb.WriteString("\n🤖 Подозрительный трафик:\n")
		for _, w := range warnings {
			sb.WriteString(w + "\n")
		}
	}

	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("🔄 Обновить", util.CBGwAnalytics+fmt.Sprintf("%d", g.ID))
	kb.AddRow().AddCallBack("← К розыгрышу", util.CBGwOpen+fmt.Sprintf("%d", g.ID))

	b.render(ctx, uid, cbID, sb.String(), kb)
}
