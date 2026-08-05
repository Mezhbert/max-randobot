package handlers

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
)

// onParticipate — обработка нажатия "Участвовать" под постом розыгрыша.
// callbackID — для снятия часиков с кнопки (AnswerCallback).
// ВАЖНО: AnswerCallback вызывается в КОНЦЕ, после отправки сообщения в диалог —
// иначе alert блокирует авто-открытие диалога при SendText/SendImage.
func (b *Bot) onParticipate(ctx context.Context, userID int64, idStr string, callbackID string) {
	defer func() {
		// Снимаем часики в самом конце — после всех отправок.
		if callbackID != "" {
			b.api.AnswerCallback(ctx, callbackID, "")
		}
	}()

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
		b.api.AnswerCallback(ctx, callbackID, "⚠️ Этот розыгрыш уже не активен.")
		return
	}

	// Проверка подписок на каналы (если есть каналы для подписки).
	if len(g.SubscribeChannelIDs) > 0 {
		missing, err := b.subs.VerifySubscriptions(ctx, userID, g)
		if err != nil {
			b.api.AnswerCallback(ctx, callbackID, "⚠️ Не удалось проверить подписку")
			_ = b.api.SendText(ctx, userID,
				"⚠️ Не удалось проверить подписку. Попробуйте позже."+fmtErr(err))
			return
		}
		if len(missing.Titles) > 0 {
			b.api.AnswerCallback(ctx, callbackID, "⚠️ Сначала подпишитесь на каналы")
			_, _ = b.api.SendTextWithKeyboard(ctx, userID,
				"Чтобы участвовать, подпишитесь на все каналы розыгрыша:\n"+missing.String(),
				util.BackKeyboard())
			return
		}
	}

	// Проверка: не участвует ли уже (идемпотентность до капчи).
	existing, _ := b.db.CountParticipant(ctx, g.ID, userID)
	if existing > 0 {
		b.api.AnswerCallback(ctx, callbackID, "✅ Вы уже участвуете!")
		return
	}

	// Капча: если включена — определяем нужна ли данному пользователю.
	if b.needCaptcha(g, userID) {
		// НЕ шлём AnswerCallback (alert) — он блокирует открытие диалога.
		// Сообщение с капчей само откроет диалог с ботом.
		b.startCaptchaChallenge(ctx, userID, g.ID)
		return
	}

	// Без капчи — регистрируем сразу.
	b.registerParticipant(ctx, userID, g, callbackID)
}

// needCaptcha определяет, нужна ли капча для данного пользователя.
func (b *Bot) needCaptcha(g store.Giveaway, userID int64) bool {
	switch g.CaptchaMode {
	case store.CaptchaAll:
		return true
	case store.CaptchaPercent:
		if g.CaptchaPercent <= 0 {
			return false
		}
		if g.CaptchaPercent >= 100 {
			return true
		}
		return rand.Intn(100) < g.CaptchaPercent
	default:
		return false
	}
}

// startCaptchaChallenge генерирует капчу, отправляет картинку и переводит
// пользователя в режим ожидания ответа (SessionCaptchaVerify).
func (b *Bot) startCaptchaChallenge(ctx context.Context, userID, giveawayID int64) {
	pngData, err := b.captcha.Generate(userID)
	if err != nil {
		_ = b.api.SendText(ctx, userID, "⚠️ Ошибка генерации капчи. Попробуйте ещё раз.")
		return
	}
	// Запоминаем, для какого розыгрыша ждём ответ.
	_ = b.db.SetSessionPayload(ctx, userID, store.SessionCaptchaVerify, map[string]any{
		"giveaway_id": giveawayID,
	})
	// Картинка капчи — отправка в диалог открывает его.
	_ = b.api.SendImage(ctx, userID, pngData, "🤖 Введите цифры с картинки, чтобы участвовать:")
}

// handleCaptchaVerifyInput проверяет ответ капчи. Возвращает true если ввод
// обработан (капча была активна).
func (b *Bot) handleCaptchaVerifyInput(ctx context.Context, userID int64, answer string) bool {
	if !b.captcha.Has(userID) {
		// Сессия есть, но капча истекла — чистим и просим нажать снова.
		_ = b.db.ClearSession(ctx, userID)
		_ = b.api.SendText(ctx, userID, "⏰ Капча истекла. Нажмите «Участвовать» ещё раз.")
		return true
	}
	answer = strings.TrimSpace(answer)
	if !b.captcha.Verify(userID, answer) {
		// Неверный ответ — новая попытка через кнопку (защита от перебора).
		_ = b.db.ClearSession(ctx, userID)
		_ = b.api.SendText(ctx, userID, "❌ Неверно! Нажмите «Участвовать» под постом, чтобы попробовать снова.")
		return true
	}
	// Верный ответ — проверяем что розыгрыш ещё активен, потом регистрируем.
	sess, _ := b.db.GetSessionPayload(ctx, userID)
	_ = b.db.ClearSession(ctx, userID)
	gidIface, _ := sess.Payload["giveaway_id"]
	gid, _ := toInt64(gidIface)
	g, err := b.db.GetGiveawayAny(ctx, gid)
	if err != nil {
		_ = b.api.SendText(ctx, userID, "⚠️ Розыгрыш не найден.")
		return true
	}
	// Капча решалась долго — розыгрыш мог завершиться. Не регистрируем.
	if g.Status != store.StatusActive {
		_ = b.api.SendText(ctx, userID, "⏰ Время розыгрыша истекло — итоги уже подведены.")
		return true
	}
	b.registerParticipant(ctx, userID, g, "")
	return true
}

// registerParticipant финализирует участие: добавляет в БД, логирует подписки,
// шлёт поздравление + alert.
func (b *Bot) registerParticipant(ctx context.Context, userID int64, g store.Giveaway, callbackID string) {
	p, err := b.db.AddParticipant(ctx, store.Participant{
		GiveawayID:    g.ID,
		UserID:        userID,
		Tickets:       1,
		CaptchaPassed: true,
	})
	if err != nil {
		// скорее всего уже участвует
		if callbackID != "" {
			b.api.AnswerCallback(ctx, callbackID, "✅ Вы уже участвуете!")
		}
		_ = b.api.SendText(ctx, userID, "Вы уже участвуете в этом розыгрыше! 🎉")
		return
	}

	// Лог подписок.
	for _, cid := range g.SubscribeChannelIDs {
		_ = b.db.AddSubscriptionLog(ctx, p.ID, g.ID, cid, userID, "post")
	}

	if callbackID != "" {
		b.api.AnswerCallback(ctx, callbackID, "🎉 Вы участвуете!")
	}
	_ = b.api.SendText(ctx, userID,
		fmt.Sprintf("🎉 Вы участвуете в розыгрыше «%s»! Удачи!", g.Name))
}

// toInt64 конвертирует any (из JSON payload) в int64.
func toInt64(v any) (int64, bool) {
	switch n := v.(type) {
	case float64:
		return int64(n), true
	case int64:
		return n, true
	case int:
		return int64(n), true
	}
	return 0, false
}
