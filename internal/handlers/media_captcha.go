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
			util.MediaStepKeyboard(len(g.MediaTokens) > 0))
		return
	}
	// Добавляем к уже сохранённым (без дублей токенов).
	all, added := mergeMediaTokens(g.MediaTokens, newTokens)
	if added == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"ℹ️ Это медиа уже добавлено в пост (всего: "+strconv.Itoa(len(all))+"). Пришлите другое или «Далее →».",
			util.MediaStepKeyboard(true))
		return
	}
	if err := b.db.UpdateGiveawayField(ctx, g.ID, "media_tokens", all); err != nil {
		b.log.Warn("media input: save", "err", err, "id", g.ID, "user", uid)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось сохранить медиа. Попробуйте позже.")
		return
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"✅ Медиа добавлено ("+strconv.Itoa(len(all))+" шт.). Можно добавить ещё или нажмите «Далее →».",
		util.MediaStepKeyboard(true))
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
