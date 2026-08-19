package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

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

	// Капча: если включена — картинка в чате (сообщение откроет диалог).
	if b.needCaptcha(g) {
		// НЕ шлём AnswerCallback (alert) заранее — сообщение с капчей
		// откроет диалог с ботом лучше алерта.
		b.startCaptchaChallenge(ctx, userID, g.ID)
		return
	}

	// Без капчи — регистрируем сразу; карточка «Вы участвуете» откроет диалог.
	b.registerParticipant(ctx, userID, g, callbackID, true)
}

// needCaptcha определяет, нужна ли капча. Только Да (all) или Нет (none) —
// процентный режим убран из UI.
func (b *Bot) needCaptcha(g store.Giveaway) bool {
	return g.CaptchaMode == store.CaptchaAll
}

// startCaptchaChallenge — капча при участии, полностью в чате бота:
// картинка с цифрами (диалог открывается самой кнопкой-ссылкой участия),
// ответ вводится сообщением (SessionCaptchaVerify).
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
	b.registerParticipant(ctx, userID, g, "", true)
	return true
}

// registerParticipant финализирует участие: добавляет в БД, логирует подписки,
// обновляет счётчик в постах. sendCard=false — полностью без сообщений от бота
// (путь мини-аппа: подтверждение рисует страница); true — одна карточка
// (legacy deep-link путь, диалог с ботом открыт самой ссылкой).
func (b *Bot) registerParticipant(ctx context.Context, userID int64, g store.Giveaway, callbackID string, sendCard bool) {
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
		if sendCard {
			_ = b.api.SendText(ctx, userID, "Вы уже участвуете в этом розыгрыше! 🎉")
		}
		return
	}

	// Лог подписок.
	for _, cid := range g.SubscribeChannelIDs {
		_ = b.db.AddSubscriptionLog(ctx, p.ID, g.ID, cid, userID, "post")
	}

	// Обновляем счётчик участников в кнопке поста (троттлинг 10с на розыгрыш).
	gCopy := g
	b.updateCounterThrottled(ctx, &gCopy)

	if callbackID != "" {
		b.api.AnswerCallback(ctx, callbackID, "🎉 Вы участвуете!")
	}
	if sendCard {
		_ = b.api.SendText(ctx, userID,
			fmt.Sprintf("🎉 Вы участвуете в розыгрыше «%s»! Удачи!", g.Name))
	}
}

// ParticipateResult — ответ API участия из мини-аппа.
type ParticipateResult struct {
	Ok         bool   // зарегистрирован / уже участвовал
	Err        string // причина отказа (подписка/статус) — пусто при Ok
	Notifiable bool   // бот может писать юзеру (диалог начат) — для opt-in
}

// ParticipateFromApp — участие через мини-апп (кнопка «Участвовать» на посте —
// open_app). Полный пайплайн: статус → подписки → регистрация. Без единого
// сообщения от бота: подтверждение рисует страница. Капча (если нужна)
// проходится на странице ДО вызова этого метода.
func (b *Bot) ParticipateFromApp(giveawayID, userID int64) ParticipateResult {
	ctx := context.Background()
	g, err := b.db.GetGiveawayAny(ctx, giveawayID)
	if err != nil {
		return ParticipateResult{Err: "Розыгрыш не найден"}
	}
	if g.Status != store.StatusActive {
		return ParticipateResult{Err: "Этот розыгрыш уже не активен"}
	}

	// Проверка подписок на каналы (если заданы).
	if len(g.SubscribeChannelIDs) > 0 {
		missing, err := b.subs.VerifySubscriptions(ctx, userID, g)
		if err != nil {
			return ParticipateResult{Err: "Не удалось проверить подписку. Попробуйте позже."}
		}
		if len(missing.Titles) > 0 {
			return ParticipateResult{Err: "Сначала подпишитесь на каналы: " + missing.String()}
		}
	}

	// Идемпотентность: уже участвует — тоже Ok (перезаход в мини-апп).
	if n, _ := b.db.CountParticipant(ctx, g.ID, userID); n > 0 {
		return ParticipateResult{Ok: true, Notifiable: b.userNotifiable(ctx, userID)}
	}

	b.registerParticipant(ctx, userID, g, "", false)
	return ParticipateResult{Ok: true, Notifiable: b.userNotifiable(ctx, userID)}
}

// userNotifiable — может ли бот писать юзеру (диалог был начат и не остановлен).
// У мини-апп-участника без старта бота=false → страница покажет opt-in кнопку.
func (b *Bot) userNotifiable(ctx context.Context, userID int64) bool {
	u, err := b.db.GetUser(ctx, userID)
	if err != nil {
		return false
	}
	return u.FlagInBase
}

// updateCounterThrottled обновляет счётчик в постах не чаще раза в 10 секунд
// на розыгрыш (защита от rps при массовом наплыве участников).
func (b *Bot) updateCounterThrottled(ctx context.Context, g *store.Giveaway) {
	if b.counterUpd == nil {
		return
	}
	now := time.Now().Unix()
	if last, ok := b.lastCounters[g.ID]; ok && now-last < 10 {
		return
	}
	b.lastCounters[g.ID] = now
	go b.counterUpd(context.Background(), g)
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
