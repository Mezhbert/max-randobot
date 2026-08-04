// Package scheduler — логика публикации розыгрышей и подведения итогов.
// Вызывается из cmd/cron (внешний cron) и из unsub-checker.
package scheduler

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/maxbot/giveawaybot/internal/maxapi"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// Scheduler содержит логику операций по расписанию.
type Scheduler struct {
	api *maxapi.API
	db  *store.Store
	log *slog.Logger
}

// New создаёт Scheduler.
func New(api *maxapi.API, db *store.Store, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{api: api, db: db, log: log}
}

// PublishPending публикует все scheduled-розыгрыши, у которых наступило publish_at.
func (s *Scheduler) PublishPending(ctx context.Context) error {
	items, err := s.db.ScheduledReady(ctx, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("list scheduled: %w", err)
	}
	for _, g := range items {
		if err := s.publishOne(ctx, g); err != nil {
			s.log.Error("publish giveaway", "id", g.ID, "err", err)
			continue
		}
	}
	return nil
}

// publishOne публикует розыгрыш во все выбранные каналы.
func (s *Scheduler) publishOne(ctx context.Context, g store.Giveaway) error {
	channels, err := s.db.ChannelsByIDs(ctx, g.OwnerUserID, g.PublishChannelIDs)
	if err != nil {
		return fmt.Errorf("load channels: %w", err)
	}
	if len(channels) == 0 {
		_ = s.db.SetStatus(ctx, g.ID, store.StatusCancelled)
		return fmt.Errorf("no publish channels")
	}

	buttons := []model.Attachment{util.ParticipateKeyboardBuilder(g.ButtonText, g.ID).Build()}
	var media []model.Attachment
	for _, m := range g.MediaTokens {
		switch m.Type {
		case "image":
			media = append(media, model.Attachment{
				Type:    model.AttachImage,
				Payload: model.Payload{Token: m.Token},
			})
		case "video":
			media = append(media, model.Attachment{
				Type:    model.AttachVideo,
				Payload: model.Payload{Token: m.Token},
			})
		}
	}
	attachments := append(media, buttons...)

	format := model.TextFormat(g.PostFormat)
	publishedAny := false
	for _, ch := range channels {
		mid, err := s.api.SendToChat(ctx, ch.ChatID, g.PostText, attachments, format)
		if err != nil {
			s.log.Warn("publish to channel", "channel", ch.ChatID, "err", err)
			continue
		}
		s.log.Info("published to channel", "giveaway", g.ID, "channel", ch.ChatID, "mid", mid)
		if err := s.db.StorePublishMessageID(ctx, g.ID, ch.ChatID, mid); err != nil {
			s.log.Warn("store publish mid", "err", err)
		}
		publishedAny = true
	}
	if !publishedAny {
		return fmt.Errorf("не удалось опубликовать ни в один канал")
	}

	// Перевод в active.
	return s.db.SetStatus(ctx, g.ID, store.StatusActive)
}

// FinishPending подводит итоги для всех active-розыгрышей, у которых наступило finish_at.
func (s *Scheduler) FinishPending(ctx context.Context) error {
	items, err := s.db.ActiveFinished(ctx, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("list active: %w", err)
	}
	for _, g := range items {
		if err := s.finishOne(ctx, g); err != nil {
			s.log.Error("finish giveaway", "id", g.ID, "err", err)
			continue
		}
	}
	return nil
}

// finishOne выбирает победителей и публикует итоги.
func (s *Scheduler) finishOne(ctx context.Context, g store.Giveaway) error {
	winners, err := s.db.PickWinners(ctx, g.ID, g.WinnersCount)
	if err != nil {
		return fmt.Errorf("pick winners: %w", err)
	}
	if err := s.db.SaveWinners(ctx, g.ID, winners); err != nil {
		return fmt.Errorf("save winners: %w", err)
	}

	// Сообщение победителям (если задано).
	if g.WinnersMessage != "" {
		for _, wid := range winners {
			_ = s.api.SendText(ctx, wid, "🎉 Вы победили в розыгрыше!\n\n"+g.WinnersMessage)
		}
	}

	// Публикация итогов.
	resultsText := s.buildResultsText(ctx, g, winners)
	if g.ResultsInOriginalPost {
		// редактируем исходные посты
		for _, mid := range g.PublishMessageIDs {
			_ = s.api.EditChatText(ctx, mid, resultsText, nil)
		}
	} else if g.ResultsPostText != "" {
		// отдельный пост в тех же каналах публикации
		channels, _ := s.db.ChannelsByIDs(ctx, g.OwnerUserID, g.PublishChannelIDs)
		for _, ch := range channels {
			mid, err := s.api.SendToChat(ctx, ch.ChatID, resultsText, nil, "")
			if err == nil {
				_ = s.db.StoreResultsMessageID(ctx, g.ID, ch.ChatID, mid)
			}
		}
	}

	return s.db.SetStatus(ctx, g.ID, store.StatusFinished)
}

func (s *Scheduler) buildResultsText(ctx context.Context, g store.Giveaway, winners []int64) string {
	names := make([]string, 0, len(winners))
	for _, wid := range winners {
		u, err := s.db.GetUser(ctx, wid)
		if err == nil {
			if u.Username != "" {
				names = append(names, "@"+u.Username)
			} else {
				names = append(names, fmt.Sprintf("[id:%d]", wid))
			}
		}
	}
	sep := ", "
	if len(names) > 5 {
		sep = "\n• "
	}
	body := g.ResultsPostText
	if body == "" {
		body = "🏁 Итоги розыгрыша"
	}
	return fmt.Sprintf("%s\n\nПобедители (%d): %s", body, len(winners), join(names, sep))
}

func join(parts []string, sep string) string {
	out := ""
	for i, p := range parts {
		if i > 0 {
			out += sep
		}
		out += p
	}
	return out
}

// CheckUnsubs перепроверяет активные подписки участников и фиксирует отписки.
func (s *Scheduler) CheckUnsubs(ctx context.Context) error {
	// Выбираем все записи subscriptions_log без unsubscribed_at для активных
	// каналов розыгрышей. Упрощённо — за последний час, по логу участников.
	rows, err := s.db.Pool().Query(ctx, `
		SELECT DISTINCT sl.user_id, sl.channel_id
		FROM subscriptions_log sl
		JOIN giveaways g ON g.id = sl.giveaway_id
		WHERE sl.unsubscribed_at IS NULL
		  AND g.status IN ('active')
		LIMIT 500
	`)
	if err != nil {
		return err
	}
	defer rows.Close()

	type pair struct{ user, channel int64 }
	var pairs []pair
	for rows.Next() {
		var p pair
		if err := rows.Scan(&p.user, &p.channel); err != nil {
			return err
		}
		pairs = append(pairs, p)
	}
	if err := rows.Err(); err != nil {
		return err
	}

	for _, p := range pairs {
		ok, err := s.api.IsSubscriber(ctx, p.channel, p.user)
		if err != nil {
			continue
		}
		if !ok {
			if err := s.db.MarkUnsubscribed(ctx, p.user, p.channel); err != nil {
				s.log.Debug("mark unsub", "err", err)
			}
		}
	}
	// keep json import used (для будущего расширения)
	return nil
}
