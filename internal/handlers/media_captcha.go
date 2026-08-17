package handlers

import (
	"context"
	"strconv"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// handleMediaInput — пользователь прислал медиа (фото/видео).
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
