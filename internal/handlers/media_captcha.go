package handlers

import (
	"context"
	"strconv"
	"strings"

	"github.com/maxbot/giveawaybot/internal/fsm"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// handleMediaInput — шаг 2: пользователь прислал медиа (фото/видео).
// Сохраняем токены вложений и остаёмся на шаге (можно добавить ещё), пока не нажмёт «Пропустить»/«Назад».
func (b *Bot) handleMediaInput(ctx context.Context, uid int64, g store.Giveaway, msg model.MessageUpdate) {
	var newTokens []store.MediaToken
	for _, at := range msg.Body.Attachments {
		switch at.Type {
		case model.AttachImage:
			if at.Payload.Token != "" {
				newTokens = append(newTokens, store.MediaToken{Token: at.Payload.Token, Type: "image"})
			}
		case model.AttachVideo:
			if at.Payload.Token != "" {
				newTokens = append(newTokens, store.MediaToken{Token: at.Payload.Token, Type: "video"})
			}
		}
	}
	if len(newTokens) == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"Не увидел фото/видео во вложениях. Пришлите медиа или нажмите «Пропустить».",
			util.NavWithSkipKeyboard())
		return
	}
	// Добавляем к уже сохранённым.
	all := append(append([]store.MediaToken{}, g.MediaTokens...), newTokens...)
	if err := b.db.UpdateGiveawayField(ctx, g.ID, "media_tokens", all); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Ошибка сохранения медиа: "+err.Error())
		return
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"✅ Медиа добавлено ("+strconv.Itoa(len(newTokens))+" шт.). Можно добавить ещё или нажать «Пропустить».",
		util.NavWithSkipKeyboard())
}

// handleCaptchaPctInput — ввод процента капчи (режим SessionCaptchaPct).
func (b *Bot) handleCaptchaPctInput(ctx context.Context, uid int64, text string) bool {
	n, err := strconv.Atoi(strings.TrimSpace(text))
	if err != nil || n < 0 || n > 100 {
		_ = b.api.SendText(ctx, uid, "⚠️ Введите число от 0 до 100.")
		return true
	}
	g, err := b.db.GetDraftGiveaway(ctx, uid)
	if err != nil {
		_ = b.db.ClearSession(ctx, uid)
		_ = b.api.SendText(ctx, uid, "Создание не активно.")
		return true
	}
	_ = b.db.UpdateGiveawayField(ctx, g.ID, "captcha_mode", store.CaptchaPercent)
	_ = b.db.UpdateGiveawayField(ctx, g.ID, "captcha_percent", n)
	_ = b.db.ClearSession(ctx, uid)
	b.advance(ctx, uid, g.ID, fsm.StepCaptcha)
	return true
}
