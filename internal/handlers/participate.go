package handlers

import (
	"context"
	"fmt"
	"strconv"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
)

// onParticipate — обработка нажатия "Участвовать" под постом розыгрыша.
func (b *Bot) onParticipate(ctx context.Context, userID int64, idStr string) {
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		_ = b.api.SendText(ctx, userID, "Розыгрыш не найден.")
		return
	}
	g, err := b.db.GetGiveawayAny(ctx, id)
	if err != nil {
		_ = b.api.SendText(ctx, userID, "Розыгрыш не найден.")
		return
	}
	if g.Status != store.StatusActive {
		_ = b.api.SendText(ctx, userID, "Этот розыгрыш уже не активен.")
		return
	}

	// Проверка подписок на каналы (если есть каналы для подписки).
	if len(g.SubscribeChannelIDs) > 0 {
		missing, err := b.subs.VerifySubscriptions(ctx, userID, g)
		if err != nil {
			_ = b.api.SendText(ctx, userID,
				"⚠️ Не удалось проверить подписку. Попробуйте позже."+fmtErr(err))
			return
		}
		if len(missing.Titles) > 0 {
			_ = b.api.SendTextWithKeyboard(ctx, userID,
				"Чтобы участвовать, подпишитесь на все каналы розыгрыша:\n"+missing.String(),
				util.BackKeyboard())
			return
		}
	}

	// Регистрация участника.
	p, err := b.db.AddParticipant(ctx, store.Participant{
		GiveawayID: g.ID,
		UserID:     userID,
		Tickets:    1,
	})
	if err != nil {
		// скорее всего уже участвует
		_ = b.api.SendText(ctx, userID, "Вы уже участвуете в этом розыгрыше! 🎉")
		return
	}

	// Лог подписок.
	for _, cid := range g.SubscribeChannelIDs {
		_ = b.db.AddSubscriptionLog(ctx, p.ID, g.ID, cid, userID, "post")
	}

	// TODO(MVP-полная): реферальная система — начисление билетов, генерация реф-ссылки.
	// TODO(MVP-полная): капча-проверка.

	_ = b.api.SendText(ctx, userID,
		fmt.Sprintf("🎉 Вы участвуете в розыгрыше «%s»! Удачи!", g.Name))
}
