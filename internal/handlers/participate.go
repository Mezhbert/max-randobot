package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// onParticipate — обработка нажатия "Участвовать" под постом розыгрыша.
// callbackID — для снятия часиков с кнопки (AnswerCallback).
// ВАЖНО: AnswerCallback вызывается в КОНЦЕ, после отправки сообщения в диалог —
// иначе alert блокирует авто-открытие диалога при SendText/SendImage.
func (b *Bot) onParticipate(ctx context.Context, userID int64, idStr string, callbackID string) {
	answered := false
	defer func() {
		// Снимаем часики в самом конце — после всех отправок (если уже ответили
		// осмысленным уведомлением — второго пустого не шлём).
		if callbackID != "" && !answered {
			b.api.AnswerCallback(ctx, callbackID, "")
		}
	}()

	// Анти-спам: не чаще раза в 3с (каждый клик = проверка подписок через API).
	if !b.partClickAllow(userID) {
		if callbackID != "" {
			answered = true
			b.api.AnswerCallback(ctx, callbackID, "Не так часто 🙂 Попробуйте через пару секунд")
		}
		return
	}

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
				"⚠️ Не удалось проверить подписку. Попробуйте позже.")
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

	// Капча: если включена — картинка + текст-подсказка в чате (текст
	// открывает диалог на клиентах, где картинка этого не делает).
	if b.needCaptcha(g) {
		if err := b.startCaptchaChallenge(ctx, userID, g.ID); err != nil {
			// Бот не может писать юзеру (диалог не начинался) — раньше «чёрная
			// дыра»: клик есть, капчи и объяснения нет. Теперь: тост-инструкция
			// + запомненный интент — при первом старте бота капча придёт САМА
			// (resumeCaptchaAfterStart), юзеру не нужно ничего вводить.
			b.log.Debug("captcha send failed (юзер не начинал диалог?)", "err", err, "user", userID)
			if callbackID != "" {
				answered = true
				b.api.AnswerCallback(ctx, callbackID,
					"🤖 Откройте чат с ботом и нажмите «Начать» — капча придёт сразу")
			}
			_ = b.db.SetSessionPayload(ctx, userID, store.SessionCaptchaWaitStart,
				map[string]any{"giveaway_id": float64(g.ID)})
			return
		}
		// Капча доставлена — тост ведёт в чат (у кого диалог не открылся сам).
		if callbackID != "" {
			answered = true
			b.api.AnswerCallback(ctx, callbackID, "🤖 Капча пришла в чат с ботом — откройте его 👆")
		}
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
func (b *Bot) startCaptchaChallenge(ctx context.Context, userID, giveawayID int64) error {
	pngData, err := b.captcha.Generate(userID)
	if err != nil {
		_ = b.api.SendText(ctx, userID, "⚠️ Ошибка генерации капчи. Попробуйте ещё раз.")
		return err
	}
	// Картинка капчи. SendImage у части клиентов НЕ открывает диалог сама
	// (текстовые сообщения открывают/скроллят, картинки — нет: подтверждено
	// живым тестом). Поэтому следом летит ТЕКСТ-подсказка: она открывает чат
	// и показывает пуш с внятным текстом — капча остаётся прямо над ней.
	if err := b.api.SendImage(ctx, userID, pngData, "🤖 Капча для участия:"); err != nil {
		return err
	}
	_ = b.api.SendText(ctx, userID, "🤖 Введите цифры с картинки чуть выше 👆 — сообщением в ответ")
	_ = b.db.SetSessionPayload(ctx, userID, store.SessionCaptchaVerify, map[string]any{
		"giveaway_id": giveawayID,
	})
	return nil
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
// обновляет счётчик в постах. sendCard=false — полностью без сообщений от бота;
// true — одна карточка (deep-link путь: диалог открыт самой ссылкой).
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

	// Рефералка: приглашённый пришёл по ссылке друга → +1 билет инвайтеру
	// (в пределах лимита) и уведомление.
	if g.ReferralEnabled {
		if inviter, ok, err := b.db.ReferralCreditIfNeeded(ctx, g.ID, userID); err == nil && ok {
			_ = b.api.SendText(ctx, inviter,
				"🤝 По вашей ссылке присоединился друг — +1 билет в розыгрыше!")
		}
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
	// Личная ссылка для приглашения друзей (рефералка включена у розыгрыша).
	if g.ReferralEnabled {
		b.sendInviteLink(ctx, userID, g)
	}
}

// sendInviteLink — личная реферальная ссылка участника (+1 билет за друга).
func (b *Bot) sendInviteLink(ctx context.Context, userID int64, g store.Giveaway) {
	code := util.RandomCode(10)
	if _, err := b.db.EnsureReferralLink(ctx, g.ID, userID, code); err != nil {
		b.log.Warn("referral link", "err", err, "user", userID, "giveaway", g.ID)
		return
	}
	botName := b.botUsername
	if botName == "" {
		botName = "ВАШ_БОТ"
	}
	link := fmt.Sprintf("https://max.ru/%s/start/ref_%s", botName, code)
	kb := model.NewKeyboard()
	kb.AddRow().AddLink("🤝 Пригласить друзей (+1 билет)", link)
	_, _ = b.api.SendTextWithKeyboard(ctx, userID,
		"🤝 Приглашайте друзей по личной ссылке:\n\n"+link+
			"\n\nЗа каждого друга, который станет участником — +1 билет вам (больше билетов = выше шанс победы).",
		kb)
}

// partClickAllow — троттл кнопки «Участвовать»: не чаще раза в 3 секунды.
func (b *Bot) partClickAllow(userID int64) bool {
	b.partMu.Lock()
	defer b.partMu.Unlock()
	now := time.Now().Unix()
	if last, ok := b.partClicks[userID]; ok && now-last < 3 {
		return false
	}
	b.partClicks[userID] = now
	if len(b.partClicks) > 10000 {
		b.partClicks = make(map[int64]int64)
	}
	return true
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
