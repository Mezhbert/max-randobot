// Package handlers — роутинг входящих update от MAX и оркестрация логики бота.
package handlers

import (
	"context"
	"fmt"
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
	botUserID   int64  // userID самого бота (для фильтра своих сообщений при /clear)
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

// SetBotUserID сохраняет userID самого бота (нужен для команды /clear —
// чтобы удалять только сообщения бота в диалоге, не трогая сообщения юзера).
func (b *Bot) SetBotUserID(id int64) {
	if id != 0 {
		b.botUserID = id
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
	case model.UpdateBotAdded:
		// Бота добавили в чат/канал (часто — как админа). Авто-сбор в «Мои каналы».
		b.onBotAdded(ctx, u)
	case model.UpdateBotRemoved:
		// Бота кикнули из чата/канала — снимаем флаг админства.
		b.onBotRemoved(ctx, u)
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

	// Команда /start. MAX при кнопке «Старт» шлёт ДВА апдейта: BotStarted
	// (его уже обработал onBotStarted — отрисовал меню) И MessageCreated с
	// текстом "/start". Чтобы меню не дублировалось — для голого /start тут
	// ничего не делаем. /start <payload> — это диплинк, обрабатываем его.
	if text == "/start" {
		return
	}
	if strings.HasPrefix(text, "/start ") {
		// Диплинк: payload обработан в OnDeepLinkStart (из bot_started).
		// Здесь просто показываем меню — без повторного «Привет».
		b.sendMainMenu(ctx, uid, "Главное меню:")
		return
	}
	if text == "/menu" {
		b.sendMainMenu(ctx, uid, "Главное меню:")
		return
	}
	// /clear — очистить диалог от сообщений бота, затем свежее меню.
	if text == "/clear" {
		b.clearDialog(ctx, uid)
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

// clearDialog — очищает диалог от сообщений бота и сбрасывает сессию юзера.
// Берёт последние ~50 сообщений диалога (диалог = чат с тем же ID, что и userID),
// удаляет те, где sender = сам бот, и сбрасывает FSM/сессию. В конце шлёт свежее меню.
// Сообщения юзера не трогаются (MAX всё равно не даёт ботам удалять чужие сообщения).
func (b *Bot) clearDialog(ctx context.Context, userID int64) {
	// Сбрасываем активную сессию и черновик розыгрыша, чтобы «начать с чистого листа».
	_ = b.db.ClearSession(ctx, userID)
	if draft, err := b.db.GetDraftGiveaway(ctx, userID); err == nil && draft.ID != 0 {
		_ = b.db.UpdateGiveawayField(ctx, draft.ID, "draft_step", "")
	}

	deleted, err := b.api.DeleteDialogMessages(ctx, userID, b.botUserID)
	if err != nil {
		b.log.Warn("clearDialog", "user", userID, "err", err, "deleted", deleted)
		// Даже если не удалось удалить — меню всё равно покажем.
		_ = b.api.SendText(ctx, userID, "Не удалось очистить диалог полностью. Вот свежее меню:")
	} else {
		_ = b.api.SendText(ctx, userID, fmt.Sprintf("🧹 Очищено сообщений: %d.", deleted))
	}
	b.sendMainMenu(ctx, userID, "Главное меню:")
}
