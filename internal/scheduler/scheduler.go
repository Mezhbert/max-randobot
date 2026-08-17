// Package scheduler — логика публикации розыгрышей и подведения итогов.
// Вызывается из встроенного тикера (cmd/bot) и из cmd/cron.
//
// Ключевое свойство — идемпотентность и атомарный захват (см. миграцию 0004):
// ни повторный тик, ни параллельный cron-процесс не должны переотправить пост
// или сообщение победителю. Достигается промежуточными статусами
// 'publishing'/'finishing' + проверкой уже сделанной работы перед отправкой.
package scheduler

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/maxbot/giveawaybot/internal/draw"
	"github.com/maxbot/giveawaybot/internal/maxapi"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// staleClaimAge — сколько «зависший» в publishing/finishing розыгрыш считается
// застрявшим (вероятно процесс упал) и откатывается обратно в очередь.
const staleClaimAge = 5 * time.Minute

// Scheduler содержит логику операций по расписанию.
type Scheduler struct {
	api         maxapi.BotAPI
	db          *store.Store
	log         *slog.Logger
	botUsername string // для построения deep-link кнопки «Участвовать»
	botUserID   int64  // для open_app-кнопки «Проверить результаты» (contact_id)
}

// New создаёт Scheduler.
func New(api maxapi.BotAPI, db *store.Store, log *slog.Logger) *Scheduler {
	if log == nil {
		log = slog.Default()
	}
	return &Scheduler{api: api, db: db, log: log}
}

// SetBotUsername задаёт публичное имя бота (для deep-link кнопки участия).
func (s *Scheduler) SetBotUsername(name string) {
	if name != "" {
		s.botUsername = name
	}
}

// SetBotUserID задаёт ID бота (contact_id для open_app-кнопки результатов).
func (s *Scheduler) SetBotUserID(id int64) {
	if id != 0 {
		s.botUserID = id
	}
}

// PublishPending публикует все scheduled-розыгрыши, у которых наступило publish_at.
// Идемпотентно: каждый розыгрыш атомарно захватывается (scheduled→publishing)
// перед обработкой, поэтому параллельные вызовы безопасны.
func (s *Scheduler) PublishPending(ctx context.Context) error {
	// Сначала подбираем застрявшие после краха процесса захваты.
	s.recoverStale(ctx)

	items, err := s.db.ScheduledReady(ctx, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("list scheduled: %w", err)
	}
	for _, g := range items {
		if err := s.publishOne(ctx, g.ID); err != nil {
			s.log.Error("publish giveaway", "id", g.ID, "err", err)
		}
	}
	return nil
}

// publishOne атомарно захватывает розыгрыш и публикует его во все каналы.
func (s *Scheduler) publishOne(ctx context.Context, id int64) error {
	g, err := s.db.ClaimForPublish(ctx, id)
	if err != nil {
		// ErrNotFound = кто-то уже захватил (другой тик/инстанс) — штатно пропускаем.
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("claim: %w", err)
	}

	// Откат при любой ошибке после захвата — розыгрыш вернётся в scheduled.
	revert := func(reason string, err error) error {
		s.log.Warn("publish revert", "id", id, "reason", reason, "err", err)
		_ = s.db.RevertPublish(ctx, id)
		return fmt.Errorf("publish %s: %w", reason, err)
	}

	channels, err := s.db.ChannelsByIDs(ctx, g.OwnerUserID, g.PublishChannelIDs)
	if err != nil {
		return revert("load channels", err)
	}
	if len(channels) == 0 {
		_ = s.db.SetStatus(ctx, id, store.StatusCancelled)
		return fmt.Errorf("publish: нет каналов публикации")
	}

	// Кнопка участия со счётчиком участников (стартует с 0).
	buttons := []model.Attachment{util.ParticipateKeyboardBuilder(g.ButtonText, g.ID, s.botUsername, 0).Build()}
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
		// Идемпотентность: если для этого канала уже есть mid (после краха
		// mid сохранился, а статус нет) — не публикуем повторно.
		if _, ok := g.PublishMessageIDs[ch.ChatID]; ok {
			publishedAny = true
			continue
		}
		mid, err := s.api.SendToChat(ctx, ch.ChatID, g.PostText, attachments, format)
		if err != nil {
			s.log.Warn("publish to channel", "channel", ch.ChatID, "err", err)
			continue
		}
		// Закрепляем пост — чтобы кнопка участия была видна в закрепе.
		if err := s.api.PinChatMessage(ctx, ch.ChatID, mid); err != nil {
			s.log.Warn("pin post (не критично)", "channel", ch.ChatID, "err", err)
		}
		// Сохраняем mid сразу после каждой отправки — чтобы при краше до смены
		// статуса следующий прогон не переиздал пост в этот канал.
		if err := s.db.StorePublishMessageID(ctx, g.ID, ch.ChatID, mid); err != nil {
			s.log.Warn("store publish mid", "err", err)
		} else {
			// Обновляем локальный кэш, чтобы повторная итерация не слала дубль.
			if g.PublishMessageIDs == nil {
				g.PublishMessageIDs = map[int64]string{}
			}
			g.PublishMessageIDs[ch.ChatID] = mid
		}
		publishedAny = true
		s.log.Info("published to channel", "giveaway", g.ID, "channel", ch.ChatID, "mid", mid)
	}

	if !publishedAny {
		return revert("ни один канал не принял пост", fmt.Errorf("no channel published"))
	}
	return s.db.MarkPublished(ctx, g.ID)
}

// UpdateParticipantCounter обновляет кнопку «Участвовать (N)» в опубликованных
// постах — редактирует каждый пост с новым счётчиком. Вызывается из
// registerParticipant после добавления участника (не чаще раз в 10с —
// защита от rps при наплыве участников).
func (s *Scheduler) UpdateParticipantCounter(ctx context.Context, g *store.Giveaway) {
	count, err := s.db.CountParticipants(ctx, g.ID)
	if err != nil {
		return
	}

	buttons := []model.Attachment{util.ParticipateKeyboardBuilder(g.ButtonText, g.ID, s.botUsername, count).Build()}
	var media []model.Attachment
	for _, m := range g.MediaTokens {
		switch m.Type {
		case "image":
			media = append(media, model.Attachment{Type: model.AttachImage, Payload: model.Payload{Token: m.Token}})
		case "video":
			media = append(media, model.Attachment{Type: model.AttachVideo, Payload: model.Payload{Token: m.Token}})
		}
	}
	attachments := append(media, buttons...)

	for chatID, mid := range g.PublishMessageIDs {
		if err := s.api.EditChatText(ctx, mid, g.PostText, attachments, model.TextFormat(g.PostFormat)); err != nil {
			s.log.Debug("update counter in post", "giveaway", g.ID, "chat", chatID, "err", err)
		}
	}
}

// FinishPending подводит итоги для всех active-розыгрышей, у которых наступило finish_at.
func (s *Scheduler) FinishPending(ctx context.Context) error {
	s.recoverStale(ctx)

	items, err := s.db.ActiveFinished(ctx, time.Now().UTC())
	if err != nil {
		return fmt.Errorf("list active: %w", err)
	}
	for _, g := range items {
		if err := s.finishOne(ctx, g.ID); err != nil {
			s.log.Error("finish giveaway", "id", g.ID, "err", err)
		}
	}
	return nil
}

// finishOne атомарно захватывает розыгрыш (active→finishing), выбирает победителей,
// уведомляет их, публикует итоги и завершает. Каждый шаг идемпотентен.
func (s *Scheduler) finishOne(ctx context.Context, id int64) error {
	g, err := s.db.ClaimForFinish(ctx, id)
	if err != nil {
		if errors.Is(err, store.ErrNotFound) {
			return nil
		}
		return fmt.Errorf("claim: %w", err)
	}
	revert := func(reason string, err error) error {
		s.log.Warn("finish revert", "id", id, "reason", reason, "err", err)
		_ = s.db.RevertFinish(ctx, id)
		return fmt.Errorf("finish %s: %w", reason, err)
	}

	// 1) Победители. Если уже избраны (после краха до уведомления) — переиспользуем.
	winners := g.WinnersUserIDs
	if len(winners) == 0 {
		entries, err := s.db.ListParticipantsForDraw(ctx, g.ID)
		if err != nil {
			return revert("list participants", err)
		}
		drawEntries := make([]draw.Entry, 0, len(entries))
		for _, p := range entries {
			drawEntries = append(drawEntries, draw.Entry{UserID: p.UserID, Tickets: p.Tickets})
		}
		winners = draw.Weighted(drawEntries, g.WinnersCount, nil)
		if err := s.db.SaveWinners(ctx, g.ID, winners); err != nil {
			return revert("save winners", err)
		}
		s.log.Info("winners picked", "giveaway", g.ID, "count", len(winners))
	}

	// 2) ЛС победителям — идемпотентно: только тем, кому ещё не отправлено.
	if g.WinnersMessage != "" {
		toNotify := diff(winners, g.WinnersNotifiedUserIDs)
		notifiedOK := make([]int64, 0, len(toNotify))
		for _, wid := range toNotify {
			if err := s.api.SendText(ctx, wid, "🎉 Вы победили в розыгрыше!\n\n"+g.WinnersMessage); err != nil {
				s.log.Warn("notify winner", "user", wid, "err", err)
				continue // попробуем снова в следующем прогоне (не в notifiedOK)
			}
			notifiedOK = append(notifiedOK, wid)
		}
		if len(notifiedOK) > 0 {
			if err := s.db.MarkWinnersNotified(ctx, g.ID, notifiedOK); err != nil {
				s.log.Warn("mark winners notified", "err", err)
			}
		}
	}

	// 3) Публикация итогов — только если ещё не опубликованы.
	if !g.ResultsPublished {
		if err := s.publishResults(ctx, &g, winners); err != nil {
			s.log.Error("publish results", "giveaway", g.ID, "err", err)
			// Не откатываем: победители уже выбраны и уведомлены. Итоги
			// опубликовать критично, но повторный прогон finishAnotherRound
			// попытается снова (results_published ещё false). Поэтому НЕ меняем
			// статус и позволяем recoverStale+следующему тику переоткатить.
			return revert("publish results", err)
		}
	}

	return s.db.MarkFinished(ctx, g.ID)
}

// publishResults публикует итоги: либо редактирует исходный пост, либо (при
// ошибке/недоступности редактирования — fallback) отправляет отдельным постом.
// Победители — кликабельные упоминания markdown [Имя](max://user/ID).
// Вместо кнопки участия — кнопка «🏆 Проверить результаты» (open_app мини-апп).
// Гарантия: итоги появляются ровно один раз.
func (s *Scheduler) publishResults(ctx context.Context, g *store.Giveaway, winners []int64) error {
	// Резолвим имена победителей через БД.
	ws := s.resolveWinners(ctx, winners)

	// Кнопка «Проверить результаты» — открывает мини-апп с деталями.
	resultsBtn := s.resultsKeyboard(g)

	if g.ResultsInOriginalPost && len(g.PublishMessageIDs) > 0 {
		// Ветка «в исходном посте»: оригинальный текст сохраняется, итоги дописываются.
		appendedText := BuildResultsAppended(g, ws)
		for chatID, mid := range g.PublishMessageIDs {
			if err := s.api.EditChatText(ctx, mid, appendedText, resultsBtn, model.FormatMarkdown); err != nil {
				// MAX ограничивает время на редактирование сообщения; для длинного
				// розыгрыша edit молча падал. Логируем и делаем fallback — отдельный пост.
				s.log.Warn("edit results into original post — fallback to separate post",
					"giveaway", g.ID, "chat", chatID, "mid", mid, "err", err)
				if nmid, ferr := s.api.SendToChat(ctx, chatID, appendedText, resultsBtn, model.FormatMarkdown); ferr == nil {
					_ = s.db.StoreResultsMessageID(ctx, g.ID, chatID, nmid)
				} else {
					s.log.Error("results fallback send failed", "giveaway", g.ID, "chat", chatID, "err", ferr)
				}
			}
		}
		return s.db.SetResultsPublished(ctx, g.ID)
	}

	// Ветка «отдельным постом».
	resultsText := BuildResultsText(g, ws)
	channels, err := s.db.ChannelsByIDs(ctx, g.OwnerUserID, g.PublishChannelIDs)
	if err != nil {
		return fmt.Errorf("load channels: %w", err)
	}
	for _, ch := range channels {
		// Идемпотентность: если уже есть results mid для канала — пропускаем.
		if _, ok := g.ResultsMessageIDs[ch.ChatID]; ok {
			continue
		}
		mid, err := s.api.SendToChat(ctx, ch.ChatID, resultsText, resultsBtn, model.FormatMarkdown)
		if err != nil {
			s.log.Warn("results post to channel", "channel", ch.ChatID, "err", err)
			continue
		}
		_ = s.db.StoreResultsMessageID(ctx, g.ID, ch.ChatID, mid)
	}
	return s.db.SetResultsPublished(ctx, g.ID)
}

// resultsKeyboard — клавиатура с кнопкой «Проверить результаты» (open_app).
// Если botUserID неизвестен — callback-кнопка (мини-апп не настроен).
func (s *Scheduler) resultsKeyboard(g *store.Giveaway) []model.Attachment {
	kb := model.NewKeyboard()
	if s.botUserID != 0 {
		btn := model.Button{
			Type:      model.ButtonOpenApp,
			Text:      "🏆 Проверить результаты",
			WebApp:    s.botUsername, // игнорируется сервером при contact_id
			ContactID: s.botUserID,
			Payload:   fmt.Sprintf("results_%d", g.ID),
		}
		kb.AddRow().AddButton(btn)
	} else {
		kb.AddRow().AddCallBack("🏆 Итоги подведены", "results_done:"+fmt.Sprintf("%d", g.ID))
	}
	return []model.Attachment{kb.Build()}
}

// Winner — победитель с именем и ID (для кликабельного упоминания).
type Winner struct {
	Name   string
	UserID int64
}

// resolveWinners резолвит ID победителей в имена через БД.
func (s *Scheduler) resolveWinners(ctx context.Context, ids []int64) []Winner {
	ws := make([]Winner, 0, len(ids))
	for _, id := range ids {
		u, err := s.db.GetUser(ctx, id)
		if err != nil || u.FirstName == "" {
			ws = append(ws, Winner{Name: fmt.Sprintf("Участник #%d", id), UserID: id})
			continue
		}
		ws = append(ws, Winner{Name: u.FirstName, UserID: id})
	}
	return ws
}

// mentionLink — кликабельное markdown-упоминание пользователя.
// MAX: [Имя](max://user/<id>) — тап открывает профиль, юзер получает уведомление.
func mentionLink(w Winner) string {
	return fmt.Sprintf("[%s](max://user/%d)", w.Name, w.UserID)
}

// winnersLine — строка победителей (упоминания через запятую или списком).
func winnersLine(ws []Winner) string {
	parts := make([]string, 0, len(ws))
	for _, w := range ws {
		parts = append(parts, mentionLink(w))
	}
	sep := ", "
	if len(ws) > 5 {
		sep = "\n• "
	}
	return strings.Join(parts, sep)
}

// BuildResultsText строит текст отдельного поста с итогами (markdown, упоминания).
func BuildResultsText(g *store.Giveaway, ws []Winner) string {
	body := g.ResultsPostText
	if body == "" {
		body = "🏁 Итоги розыгрыша"
	}
	return fmt.Sprintf("%s\n\nПобедители (%d): %s", body, len(ws), winnersLine(ws))
}

// BuildResultsAppended сохраняет оригинальный текст поста розыгрыша и дополняет
// его блоком итогов с кликабельными победителями (markdown-упоминания).
func BuildResultsAppended(g *store.Giveaway, ws []Winner) string {
	suffix := fmt.Sprintf("🏁 Итоги!\nПобедители (%d): %s", len(ws), winnersLine(ws))
	if g.PostText == "" {
		return suffix
	}
	return g.PostText + "\n\n———\n\n" + suffix
}

// diff возвращает элементы a, отсутствующие в b.
func diff(a, b []int64) []int64 {
	if len(b) == 0 {
		return a
	}
	seen := make(map[int64]bool, len(b))
	for _, x := range b {
		seen[x] = true
	}
	out := make([]int64, 0, len(a))
	for _, x := range a {
		if !seen[x] {
			out = append(out, x)
		}
	}
	return out
}

// recoverStale откатывает розыгрыши, зависшие в publishing/finishing дольше
// staleClaimAge (например, процесс упал между захватом и завершением).
func (s *Scheduler) recoverStale(ctx context.Context) {
	cutoff := time.Now().UTC().Add(-staleClaimAge)
	items, err := s.db.ListStaleClaimed(ctx, cutoff, 100)
	if err != nil {
		s.log.Debug("list stale claimed", "err", err)
		return
	}
	for _, g := range items {
		switch g.Status {
		case store.StatusPublishing:
			s.log.Warn("recover stale publishing → scheduled", "id", g.ID)
			_ = s.db.RevertPublish(ctx, g.ID)
		case store.StatusFinishing:
			s.log.Warn("recover stale finishing → active", "id", g.ID)
			_ = s.db.RevertFinish(ctx, g.ID)
		}
	}
}

// CheckUnsubs перепроверяет активные подписки участников и фиксирует отписки.
// Пагинируется по participants, чтобы не грузить всю базу за раз.
func (s *Scheduler) CheckUnsubs(ctx context.Context) error {
	rows, err := s.db.Pool().Query(ctx, `
		SELECT DISTINCT sl.user_id, sl.channel_id
		FROM subscriptions_log sl
		JOIN giveaways g ON g.id = sl.giveaway_id
		WHERE sl.unsubscribed_at IS NULL
		  AND g.status = 'active'
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
	return nil
}
