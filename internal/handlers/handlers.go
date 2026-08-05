// Package handlers — роутинг входящих update от MAX и оркестрация логики бота.
package handlers

import (
	"context"
	"log/slog"
	"strings"

	"github.com/maxbot/giveawaybot/internal/captcha"
	"github.com/maxbot/giveawaybot/internal/dedup"
	"github.com/maxbot/giveawaybot/internal/fsm"
	"github.com/maxbot/giveawaybot/internal/maxapi"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/subscription"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// Bot — центральный обработчик. Один экземпляр на приложение.
type Bot struct {
	api         maxapi.BotAPI
	db          *store.Store
	engine      *fsm.Engine
	subs        *subscription.Checker
	captcha     *captcha.Service
	log         *slog.Logger
	botUsername string             // публичное имя бота (для UTM-диплинков)
	botUserID   int64              // userID бота (для фильтра своих сообщений при /clear)
	dedup       *dedup.Deduplicator // опционально; nil — без дедупликации
}

// New создаёт обработчика.
func New(api maxapi.BotAPI, db *store.Store, log *slog.Logger) *Bot {
	if log == nil {
		log = slog.Default()
	}
	return &Bot{
		api:    api,
		db:     db,
		engine: fsm.New(db, log),
		subs:   subscription.New(api, db, log),
		captcha: captcha.New(0),
		log:    log,
	}
}

// SetUsername задаёт публичное имя бота (для построения диплинков UTM).
func (b *Bot) SetUsername(name string) {
	if name != "" {
		b.botUsername = strings.TrimPrefix(name, "@")
	}
}

// SetBotUserID сохраняет userID самого бота (для /clear — фильтр своих сообщений).
func (b *Bot) SetBotUserID(id int64) {
	if id != 0 {
		b.botUserID = id
	}
}

// SetDedup подключает дедупликатор апдейтов (защита от повторной обработки
// ретраев webhook от MAX).
func (b *Bot) SetDedup(d *dedup.Deduplicator) {
	b.dedup = d
}

// Handle — точка входа для каждого update (вызывается из webhook-сервера).
func (b *Bot) Handle(ctx context.Context, u model.Update) {
	// Дедупликация: MAX ретраит webhook при медленном ответе, из-за чего один
	// апдейт может прийти дважды. Отбрасываем уже виденный.
	if b.dedup != nil {
		if key := dedup.Key(u); key != "" && b.dedup.SeenOrMark(key) {
			b.log.Debug("duplicate update dropped", "type", u.UpdateType)
			return
		}
	}

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
		// Запоминаем chat_id диалога (нужен для /clear — MAX API требует chat_id, не user_id).
		if u.ChatID != 0 {
			_ = b.db.SetDialogChatID(ctx, u.UserID, u.ChatID)
		}
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
// Показываем информативный стартовый экран с описанием функций.
func (b *Bot) onBotStarted(ctx context.Context, u model.Update) {
	b.sendStartScreen(ctx, u.UserID)
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

	// /start — информативное приветствие с описанием бота + меню.
	if text == "/start" || strings.HasPrefix(text, "/start ") {
		b.sendStartScreen(ctx, uid)
		return
	}
	// /menu — только меню, без описания.
	if text == "/menu" {
		b.sendMainMenu(ctx, uid, "Главное меню:")
		return
	}
	// /clear — очистить чат с ботом (удалить сообщения бота), затем свежее меню.
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
	case store.SessionCaptchaVerify:
		if b.handleCaptchaVerifyInput(ctx, uid, text) {
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
	_, _ = b.api.SendTextWithKeyboard(ctx, userID, text, util.MainMenuKeyboard())
}

// sendStartScreen — информативное приветствие при /start: что умеет бот + меню.
func (b *Bot) sendStartScreen(ctx context.Context, userID int64) {
	text := `🎁 Бот розыгрышей в MAX

Что умеет:
• Создавать розыгрыши с постами, медиа и кнопкой «Участвовать»
• Публиковать в каналах по расписанию, проверять подписки участников
• Подводить итоги: взвешенный выбор победителей, итоги в посте или отдельным
• Капча от ботов (картинка с цифрами при участии)
• UTM-аналитика источников подписчиков с экспортом CSV

Команды:
/menu — главное меню
/clear — очистить этот чат

Выберите действие 👇`
	_, _ = b.api.SendTextWithKeyboard(ctx, userID, text, util.MainMenuKeyboard())
}

// clearDialog удаляет сообщения бота в диалоге и показывает свежее меню.
func (b *Bot) clearDialog(ctx context.Context, userID int64) {
	// Сбрасываем активную сессию — «начать с чистого листа».
	_ = b.db.ClearSession(ctx, userID)

	// MAX API для GetMessages требует chat_id диалога, не user_id.
	// Берём сохранённый chat_id из БД (сохраняется при каждом апдейте от юзера).
	chatID, _ := b.db.GetDialogChatID(ctx, userID)
	if chatID == 0 {
		// chat_id ещё не сохранён (юзер ни разу не писал) — не можем очистить.
		b.log.Warn("clearDialog: dialog_chat_id не найден", "user", userID)
		b.sendMainMenu(ctx, userID, "🧹 Не удалось определить чат для очистки. Вот свежее меню:")
		return
	}

	deleted, err := b.api.DeleteDialogMessages(ctx, chatID, b.botUserID)
	if err != nil {
		b.log.Warn("clearDialog", "user", userID, "chat", chatID, "err", err, "deleted", deleted)
	}
	b.log.Info("clearDialog", "user", userID, "chat", chatID, "deleted", deleted)
	// Свежее меню (после очистки — это единственное сообщение бота).
	b.sendMainMenu(ctx, userID, "🧹 Чат очищен. Главное меню:")
}
