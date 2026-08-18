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
// Повторное добавление того же вложения (после «Назад») не дублирует токен.
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
	// Добавляем к уже сохранённым (без дублей токенов).
	all, added := mergeMediaTokens(g.MediaTokens, newTokens)
	if added == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"ℹ️ Это медиа уже добавлено в пост (всего: "+strconv.Itoa(len(all))+"). Пришлите другое или «Пропустить».",
			util.NavWithSkipKeyboard())
		return
	}
	if err := b.db.UpdateGiveawayField(ctx, g.ID, "media_tokens", all); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Ошибка сохранения медиа: "+err.Error())
		return
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"✅ Медиа добавлено ("+strconv.Itoa(len(all))+" шт.). Можно добавить ещё или нажать «Пропустить».",
		util.NavWithSkipKeyboard())
}

// mergeMediaTokens объединяет существующие и новые медиа-токены без дублей
// (токен одного вложения уникален). Возвращает итог и число добавленных.
func mergeMediaTokens(existing, incoming []store.MediaToken) ([]store.MediaToken, int) {
	seen := make(map[string]bool, len(existing))
	all := make([]store.MediaToken, 0, len(existing)+len(incoming))
	for _, m := range existing {
		if m.Token != "" && !seen[m.Token] {
			seen[m.Token] = true
			all = append(all, m)
		}
	}
	added := 0
	for _, m := range incoming {
		if m.Token != "" && !seen[m.Token] {
			seen[m.Token] = true
			all = append(all, m)
			added++
		}
	}
	return all, added
}
