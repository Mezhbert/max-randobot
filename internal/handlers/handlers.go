// Package handlers — роутинг входящих update от MAX и оркестрация логики бота.
package handlers

import (
	"context"
	"log/slog"
	"strings"

	"github.com/maxbot/giveawaybot/internal/fsm"
	"github.com/maxbot/giveawaybot/internal/maxapi"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/subscription"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// Bot — центральный обработчик. Один экземпляр на приложение.
type Bot struct {
	api         *maxapi.API
	db          *store.Store
	engine      *fsm.Engine
	subs        *subscription.Checker
	log         *slog.Logger
	botUsername string // публичное имя бота (для UTM-диплинков)
}

// New создаёт обработчика.
func New(api *maxapi.API, db *store.Store, log *slog.Logger) *Bot {
	if log == nil {
		log = slog.Default()
	}
	return &Bot{
		api:    api,
		db:     db,
		engine: fsm.New(db, log),
		subs:   subscription.New(api, db, log),
		log:    log,
	}
}

// SetUsername задаёт публичное имя бота (для построения диплинков UTM).
func (b *Bot) SetUsername(name string) {
	if name != "" {
		b.botUsername = strings.TrimPrefix(name, "@")
	}
}

// Handle — точка входа для каждого update (вызывается из webhook-сервера).
func (b *Bot) Handle(ctx context.Context, u model.Update) {
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("panic in handler", "err", r, "type", u.UpdateType)
		}
	}()

	// Сохраняем/обновляем пользователя в базе (ТЗ: все попадают в БД бота).
	if u.UserID != 0 {
		usr := u.GetUser()
		_ = b.db.UpsertUser(ctx, store.User{
			UserID:    u.UserID,
			FirstName: usr.FirstName,
			Username:  usr.Username,
			IsBot:     usr.IsBot,
		})
	}

	switch u.UpdateType {
	case model.UpdateBotStarted:
		b.onBotStarted(ctx, u)
	case model.UpdateBotStopped:
		// Юзер остановил диалог — больше нельзя ему писать (ТЗ: flag_in_base).
		if u.UserID != 0 {
			_ = b.db.SetFlagInBase(ctx, u.UserID, false)
		}
	case model.UpdateMessageCreated:
		b.onMessageCreated(ctx, u)
	case model.UpdateMessageCallback:
		b.onMessageCallback(ctx, u)
	case model.UpdateUserAdded:
		b.onUserAdded(ctx, u)
	case model.UpdateUserRemoved:
		b.onUserRemoved(ctx, u)
	case model.UpdateMessageEdited, model.UpdateMessageRemoved:
		// пока не используется
	default:
		b.log.Debug("unhandled update", "type", u.UpdateType)
	}
}

// onBotStarted — пользователь запустил бота (или по диплинку ?start=...).
func (b *Bot) onBotStarted(ctx context.Context, u model.Update) {
	b.sendMainMenu(ctx, u.UserID, "👋 Привет! Это бот для розыгрышей в MAX. Выберите действие:")
}

// OnDeepLinkStart — вызывается из main.go при bot_started с payload диплинка.
// body — raw JSON вебхука (для извлечения user_id), payload — значение диплинка.
func (b *Bot) OnDeepLinkStart(ctx context.Context, body []byte, payload string) {
	defer func() {
		if r := recover(); r != nil {
			b.log.Error("panic in OnDeepLinkStart", "err", r)
		}
	}()
	uid := maxapi.ParseStartedUserID(body)
	if uid == 0 {
		return
	}
	b.onBotStartedPayload(ctx, uid, payload)
}

// onMessageCreated — текстовое сообщение пользователя.
func (b *Bot) onMessageCreated(ctx context.Context, u model.Update) {
	uid := u.UserID
	msg := u.GetMessage()
	text := strings.TrimSpace(msg.Body.Text)

	// Команда /start → в меню
	if text == "/start" || strings.HasPrefix(text, "/start ") {
		b.sendMainMenu(ctx, uid, "Главное меню:")
		return
	}
	if text == "/menu" {
		b.sendMainMenu(ctx, uid, "Главное меню:")
		return
	}

	// Состояние юзера вне FSM (добавление канала и т.п.) — приоритет.
	session, _ := b.db.GetSession(ctx, uid)
	switch session {
	case store.SessionAddingChannel:
		if b.handleAddChannelInput(ctx, uid, msg) {
			return
		}
	case store.SessionCaptchaPct:
		if b.handleCaptchaPctInput(ctx, uid, text) {
			return
		}
	case store.SessionUTMCreate:
		if b.handleUTMCreateInput(ctx, uid, text) {
			return
		}
	}

	// Если идёт создание розыгрыша — отдать ввод в FSM
	if draft, err := b.db.GetDraftGiveaway(ctx, uid); err == nil && draft.DraftStep != "" {
		// Шаг медиа обрабатываем по attachments, а не по тексту.
		if draft.DraftStep == fsm.StepMedia {
			b.handleMediaInput(ctx, uid, draft, msg)
			return
		}
		b.handleDraftInput(ctx, uid, draft, text)
		return
	}

	// Иначе — эхо-подсказка
	_ = b.api.SendText(ctx, uid, "Используйте кнопки меню. Для вызова меню отправьте /menu")
}

// sendMainMenu отправляет главное меню пользователю.
func (b *Bot) sendMainMenu(ctx context.Context, userID int64, text string) {
	_ = b.api.SendTextWithKeyboard(ctx, userID, text, util.MainMenuKeyboard())
}
