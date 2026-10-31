// Package handlers — роутинг входящих update от MAX и оркестрация логики бота.
package handlers

import (
	"context"
	"log/slog"
	"strings"
	"sync"

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
	botUsername  string // публичное имя бота (для диплинков UTM/участия)
	helpURL      string // публичный URL справки (/app/help.html) для link-кнопки
	publicURL    string // публичный HTTPS-домен бота — база бесшовных /r/-ссылок
	counterUpd   func(ctx context.Context, g *store.Giveaway) // обновление счётчика участников в постах (сеттится из main, опционально)
	publishNow   func(ctx context.Context, giveawayID int64) // немедленная публикация после Принять (сеттится из main)
	lastCounters map[int64]int64                                // giveaway_id → unix-time последнего обновления счётчика (троттлинг)
	dedup        *dedup.Deduplicator                            // опционально; nil — без дедупликации
	pendingMu    sync.Mutex                                     // защита pendingStarts
	pendingStarts map[int64]string                              // user_id → payload диплинка (до доставки bot_started в Handle)
	partMu       sync.Mutex                                     // защита partClicks (троттл «Участвовать»)
	partClicks   map[int64]int64                                // user_id → unix последнего клика участия
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
		pendingStarts: make(map[int64]string),
		partClicks:   make(map[int64]int64),
	}
}

// SetUsername задаёт публичное имя бота (для построения диплинков UTM/участия).
func (b *Bot) SetUsername(name string) {
	if name != "" {
		b.botUsername = strings.TrimPrefix(name, "@")
	}
}

// SetHelpURL задаёт публичный URL справки (WEBHOOK_URL + /app/help.html).
func (b *Bot) SetHelpURL(url string) {
	if url != "" {
		b.helpURL = url
	}
}

// SetPublicURL задаёт публичный домен (WEBHOOK_URL) — для бесшовных
// трекинг-ссылок вида https://<домен>/r/<code>.
func (b *Bot) SetPublicURL(url string) {
	if url != "" {
		b.publicURL = strings.TrimRight(url, "/")
	}
}

// SetCounterUpdater подключает функцию обновления счётчика участников
// в опубликованных постах (вызывается из main со ссылкой на Scheduler).
func (b *Bot) SetCounterUpdater(f func(ctx context.Context, g *store.Giveaway)) {
	b.counterUpd = f
}

// SetPublishNow подключает немедленную публикацию розыгрыша (режим
// «Опубликовать сразу» — после Принять, без ожидания тика планировщика).
func (b *Bot) SetPublishNow(f func(ctx context.Context, giveawayID int64)) {
	b.publishNow = f
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
	case model.UpdateBotAdded, model.UpdateBotRemoved:
		// Бота добавили/убрали админом в канале — сразу обновляем флаг у всех
		// владельцев этого канала (канал может быть добавлен нескольким юзерам).
		if u.ChatID != 0 {
			isAdmin := u.UpdateType == model.UpdateBotAdded
			if err := b.db.SetChannelAdminByChat(ctx, u.ChatID, isAdmin); err != nil {
				b.log.Debug("bot_added/removed: update channel flag", "chat", u.ChatID, "err", err)
			}
		}
	case model.UpdateMessageEdited, model.UpdateMessageRemoved:
		// пока не используется
	default:
		b.log.Debug("unhandled update", "type", u.UpdateType)
	}
}

// onBotStarted — пользователь запустил бота (или по диплинку ?start=...).
// Диплинк (кнопка участия part_/UTM): юзер «провалился» в диалог из канала —
// стартовый экран не шлём (шум: следующим сообщением бот скажет, что делать —
// капча/регистрация). Обычный /start — информативный экран с меню.
func (b *Bot) onBotStarted(ctx context.Context, u model.Update) {
	// Повторный старт снова разрешает боту писать (bot_stopped снимал флаг).
	_ = b.db.SetFlagInBase(ctx, u.UserID, true)

	if p := b.takePendingStart(u.UserID); p != "" {
		b.onBotStartedPayload(ctx, u.UserID, p)
		return
	}
	// Капча ждала начала диалога (юзер пришёл с кнопки участия, бот не мог
	// писать) — отправляем её сразу, без ввода /start и без стартового экрана.
	if b.resumeCaptchaAfterStart(ctx, u.UserID) {
		return
	}
	b.sendStartScreen(ctx, u.UserID)
}

// resumeCaptchaAfterStart — у юзера была «отложенная» капча: диалог открыт,
// теперь бот может писать. true — капча отправлена.
func (b *Bot) resumeCaptchaAfterStart(ctx context.Context, userID int64) bool {
	sess, err := b.db.GetSessionPayload(ctx, userID)
	if err != nil || sess.State != store.SessionCaptchaWaitStart {
		return false
	}
	gidF, _ := sess.Payload["giveaway_id"].(float64)
	gid := int64(gidF)
	g, err := b.db.GetGiveawayAny(ctx, gid)
	if err != nil || g.Status != store.StatusActive {
		_ = b.db.ClearSession(ctx, userID)
		return false
	}
	if err := b.startCaptchaChallenge(ctx, userID, gid); err != nil {
		_ = b.db.ClearSession(ctx, userID)
		return false
	}
	return true
}

// SetPendingStart запоминает payload диплинка до доставки апдейта в Handle.
// Вызывается из webhook-сервера СИНХРОННО (до передачи body в SDK-хендлер):
// к моменту `go bot.Handle(...)` payload уже ждёт — гонки двух горутин нет.
// Нужен потому, что SDK-модель Update не парсит поле payload у bot_started.
func (b *Bot) SetPendingStart(userID int64, payload string) {
	b.pendingMu.Lock()
	defer b.pendingMu.Unlock()
	if len(b.pendingStarts) > 10000 { // защита от утечки (апдейт не дошёл)
		b.pendingStarts = make(map[int64]string)
	}
	b.pendingStarts[userID] = payload
}

// takePendingStart забирает и очищает payload диплинка юзера ("" — нет).
func (b *Bot) takePendingStart(userID int64) string {
	b.pendingMu.Lock()
	defer b.pendingMu.Unlock()
	p, ok := b.pendingStarts[userID]
	if ok {
		delete(b.pendingStarts, userID)
	}
	return p
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
	// /stop — явная остановка активного действия: отменить создание
	// (эквивалент кнопки «✖ Отменить»), сбросить висящую сессию
	// (добавление канала/капча/UTM/редактура) и вернуться в меню.
	if text == "/stop" {
		msg := "⏹ Нечего останавливать."
		if g, err := b.db.GetDraftGiveaway(ctx, uid); err == nil {
			_ = b.engine.Abandon(ctx, g.ID)
			// Черновик без названия — мусор, удаляем (как в «✖ Отменить»).
			if g.Name != "" {
				msg = "⏹ Создание остановлено. Черновик сохранён в «Мои розыгрыши → Черновики»."
			} else {
				_ = b.db.DeleteGiveaway(ctx, g.ID, uid)
				msg = "⏹ Создание остановлено."
			}
		} else if session, _ := b.db.GetSession(ctx, uid); session != "" {
			msg = "⏹ Действие сброшено."
		}
		_ = b.db.ClearSession(ctx, uid)
		b.sendMainMenu(ctx, uid, msg)
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
	case store.SessionEditMedia:
		if b.handleEditMediaInput(ctx, uid, msg) {
			return
		}
	}

	// Если идёт создание розыгрыша — валидируем ввод под тип шага.
	if draft, err := b.db.GetDraftGiveaway(ctx, uid); err == nil && draft.DraftStep != "" {
		// Шаг медиа обрабатываем по attachments (форвард с фото/видео — ок).
		if draft.DraftStep == fsm.StepMedia {
			b.handleMediaInput(ctx, uid, draft, msg)
			return
		}
		// Шаги, где выбор делается КНОПКАМИ (каналы/режимы/капча/подтверждение):
		// текст и форварды не принимаем — объясняем и перерисовываем шаг.
		if fsm.IsButtonStep(draft.DraftStep) {
			if text != "" || isForward(msg) || hasAttachments(msg) {
				b.rejectUnexpectedDraftInput(ctx, uid, draft)
				return
			}
			// Пустое сообщение без вложений — тоже малополезно, подсказываем.
			b.rejectUnexpectedDraftInput(ctx, uid, draft)
			return
		}
		// Текстовые шаги: форвард поста — не тот ввод (текст нужен свой).
		if isForward(msg) {
			_, _ = b.api.SendTextWithKeyboard(ctx, uid,
				"⚠️ Это пересланное сообщение, а на этом шаге нужно ввести СВОЙ текст.\n\n"+
					fsm.PromptForStep(draft.DraftStep),
				kbForStep(draft.DraftStep, draft))
			return
		}
		b.handleDraftInput(ctx, uid, draft, text)
		return
	}

	// Fallback: подпись кнопки меню как текст (напр. скопированная).
	if payload := util.ReplyPayloadByText(text); payload != "" {
		b.handleMenuAction(ctx, uid, payload)
		return
	}

	// Ничего не поняли — объясняем, что бот умеет сейчас (контекстная подсказка).
	b.unexpectedNoFlow(ctx, uid, msg)
}

// isForward — пересланное сообщение (из канала/чата).
func isForward(msg model.MessageUpdate) bool {
	return msg.Link != nil && msg.Link.Type == model.LinkTypeForward && msg.Link.ChatID != 0
}

// hasAttachments — есть ли у сообщения вложения (медиа/файлы).
func hasAttachments(msg model.MessageUpdate) bool {
	return len(msg.Body.Attachments) > 0
}

// unexpectedNoFlow — пользователь прислал что-то неожидаемое вне любого флоу
// (нет сессии, нет создания): объясняем и показываем меню.
func (b *Bot) unexpectedNoFlow(ctx context.Context, uid int64, msg model.MessageUpdate) {
	switch {
	case isForward(msg):
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"🤔 Не понял: вы переслали пост.\n\n"+
				"Если хотите ДОБАВИТЬ КАНАЛ — «Мои каналы → ➕ Добавить канал», затем перешлите пост ещё раз.\n"+
				"Для остального — кнопки меню ниже 👇",
			util.MainMenuKeyboard())
	case hasAttachments(msg):
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"🤔 Не понял: сообщение с вложением.\n\nВыберите действие кнопками меню ниже 👇",
			util.MainMenuKeyboard())
	default:
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"🤔 Не понял команду.\n\nВыберите действие кнопками меню ниже 👇\n"+
				"Меню также вызывается командой /menu.",
			util.MainMenuKeyboard())
	}
}

// rejectUnexpectedDraftInput — на кнопочном шаге пришёл текст/форвард:
// объясняем и заново показываем шаг с кнопками.
func (b *Bot) rejectUnexpectedDraftInput(ctx context.Context, uid int64, g store.Giveaway) {
	var what string
	switch g.DraftStep {
	case fsm.StepSubChannels, fsm.StepPubChannels:
		what = "выбрать каналы кнопками из списка"
	case fsm.StepPreview:
		what = "подтвердить кнопками «✅ Принять» / «❌ Отклонить»"
	default:
		what = "выбрать вариант кнопкой"
	}
	_ = b.api.SendText(ctx, uid, "⚠️ Здесь не нужен текст — нужно "+what+". Вот шаг ещё раз 👇")
	b.showStep(ctx, uid, g, g.DraftStep)
}

// sendMainMenu отправляет главное меню пользователю.
//
// NOTE: reply-keyboard (кнопки у поля ввода) в MAX для ботов НЕ работает:
// платформа принимает attachment (200 OK), но молча выкидывает его —
// сообщение доходит голым текстом (проверено живым API-запросом и чтением
// сообщения обратно: attachments пуст). Поэтому меню — всегда inline.
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
/stop — остановить создание и сбросить состояние

Выберите действие 👇`
	_, _ = b.api.SendTextWithKeyboard(ctx, userID, text, util.MainMenuKeyboard())
}
