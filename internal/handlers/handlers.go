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
	api          maxapi.BotAPI
	db           *store.Store
	engine       *fsm.Engine
	subs         *subscription.Checker
	captcha      *captcha.Service
	log          *slog.Logger
	botUsername  string // публичное имя бота (для UTM-диплинков)
	counterUpd   func(ctx context.Context, g *store.Giveaway) // обновление счётчика участников в постах (сеттится из main, опционально)
	lastCounters map[int64]int64                                // giveaway_id → unix-time последнего обновления счётчика (троттлинг)
	dedup        *dedup.Deduplicator                            // опционально; nil — без дедупликации
}

// New создаёт обработчика.
func New(api maxapi.BotAPI, db *store.Store, log *slog.Logger) *Bot {
	if log == nil {
		log = slog.Default()
	}
	return &Bot{
		api:          api,
		db:           db,
		engine:       fsm.New(db, log),
		subs:         subscription.New(api, db, log),
		captcha:      captcha.New(0),
		log:          log,
		lastCounters: make(map[int64]int64),
	}
}

// SetUsername задаёт публичное имя бота (для построения диплинков UTM).
func (b *Bot) SetUsername(name string) {
	if name != "" {
		b.botUsername = strings.TrimPrefix(name, "@")
	}
}

// SetCounterUpdater подключает функцию обновления счётчика участников
// в опубликованных постах (вызывается из main со ссылкой на Scheduler).
func (b *Bot) SetCounterUpdater(f func(ctx context.Context, g *store.Giveaway)) {
	b.counterUpd = f
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

	// Состояние юзера вне FSM (добавление канала и т.п.) — приоритет.
	session, _ := b.db.GetSession(ctx, uid)
	switch session {
	case store.SessionAddingChannel:
		if b.handleAddChannelInput(ctx, uid, msg) {
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
	case store.SessionEditField:
		if b.handleEditFieldInput(ctx, uid, text) {
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

// render рисует экран в ТОМ ЖЕ сообщении, где юзер нажал кнопку
// (CallbackAnswer.Message = edit-in-place). Ответ появляется под пальцем —
// скролл не нужен, диалог не пачкается новым сообщением на каждый клик.
// Если callbackID пуст (вход командой/текстом) или edit упал (истёк срок
// редактирования и т.п.) — fallback: обычная отправка нового сообщения.
func (b *Bot) render(ctx context.Context, uid int64, callbackID, text string, kb *model.Keyboard) {
	if callbackID != "" {
		if err := b.api.AnswerCallbackEdit(ctx, callbackID, text, kb); err == nil {
			return
		} else {
			b.log.Debug("callback edit failed, fallback to send", "err", err)
		}
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, text, kb)
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

Выберите действие 👇`
	_, _ = b.api.SendTextWithKeyboard(ctx, userID, text, util.MainMenuKeyboard())
}

// renderMainMenu — главное меню edit-in-place (по клику) или отправкой (команда).
func (b *Bot) renderMainMenu(ctx context.Context, uid int64, cbID, text string) {
	b.render(ctx, uid, cbID, text, util.MainMenuKeyboard())
}
