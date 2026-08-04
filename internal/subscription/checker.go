// Package subscription — проверка подписок участника на каналы розыгрыша.
// Использует GetMembers с user_ids для точечной проверки одного пользователя,
// что держит запросы в рамках лимита платформы MAX (30 rps).
package subscription

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/maxbot/giveawaybot/internal/maxapi"
	"github.com/maxbot/giveawaybot/internal/store"
)

// Checker проверяет подписки пользователя на каналы.
type Checker struct {
	api *maxapi.API
	db  *store.Store
	log *slog.Logger
}

// New создаёт проверщика.
func New(api *maxapi.API, db *store.Store, log *slog.Logger) *Checker {
	if log == nil {
		log = slog.Default()
	}
	return &Checker{api: api, db: db, log: log}
}

// MissingChannels — список каналов, на которые юзер ещё не подписан.
type MissingChannels struct {
	Titles []string
}

// String — человекочитаемый список.
func (m MissingChannels) String() string {
	if len(m.Titles) == 0 {
		return ""
	}
	var b strings.Builder
	for _, t := range m.Titles {
		b.WriteString("• ")
		b.WriteString(t)
		b.WriteString("\n")
	}
	return b.String()
}

// VerifySubscriptions проверяет, что userID подписан на все каналы розыгрыша.
// Возвращает список каналов, на которые юзер НЕ подписан (пусто = всё ок).
func (c *Checker) VerifySubscriptions(ctx context.Context, userID int64, g store.Giveaway) (MissingChannels, error) {
	missing := MissingChannels{}
	if len(g.SubscribeChannelIDs) == 0 {
		return missing, nil
	}
	channels, err := c.db.ChannelsByIDs(ctx, g.OwnerUserID, g.SubscribeChannelIDs)
	if err != nil {
		return missing, fmt.Errorf("load channels: %w", err)
	}
	for _, ch := range channels {
		ok, err := c.api.IsSubscriber(ctx, ch.ChatID, userID)
		if err != nil {
			c.log.Warn("IsSubscriber", "err", err, "channel", ch.ChatID, "user", userID)
			// При ошибке не блокируем участие — логируем, считаем что подписан.
			// Это сознательное решение: лучше пропустить, чем ложно отказать.
			continue
		}
		if !ok {
			title := ch.Title
			if ch.Link != "" {
				title = fmt.Sprintf("%s (%s)", ch.Title, ch.Link)
			}
			if title == "" {
				title = fmt.Sprintf("канал #%d", ch.ChatID)
			}
			missing.Titles = append(missing.Titles, title)
		}
	}
	return missing, nil
}
