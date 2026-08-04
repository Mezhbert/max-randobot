// Package maxapi — обёртка над max-bot-api-client-go с rate-limit'ом 30 rps
// (требование платформы MAX) и удобными хелперами.
package maxapi

import (
	"context"
	"encoding/json"
	"log/slog"
	"net/http"
	"sync"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// API оборачивает официальный клиент, добавляя rate-limit и хелперы.
type API struct {
	*maxbot.Api
	rl *rateLimiter
}

// New создаёт обёртку. maxRPS — целевой лимит (платформа: 30).
// httpClient опционален: если nil — используется дефолт библиотеки; иначе
// применяется кастомный (например, с добавленным корневым сертификатом
// Минцифры для работы с platform-api2.max.ru).
func New(token string, maxRPS int, httpClient *http.Client) (*API, error) {
	opts := []maxbot.Opt{}
	if httpClient != nil {
		opts = append(opts, maxbot.WithHTTPClient(httpClient))
	}
	base, err := maxbot.NewApi(token, opts...)
	if err != nil {
		return nil, err
	}
	return &API{Api: base, rl: newRateLimiter(maxRPS)}, nil
}

// ParseStartedPayload достаёт поле "payload" диплинка из raw JSON тела webhook
// для update_type=bot_started. Либа max-bot-api-client-go НЕ парсит это поле,
// поэтому читаем его сами до отдачи body в GetHandler.
// Возвращает ("", nil) если это не bot_started или payload отсутствует.
func ParseStartedPayload(body []byte) string {
	var probe struct {
		UpdateType string `json:"update_type"`
		Payload    string `json:"payload"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return ""
	}
	if probe.UpdateType != string(model.UpdateBotStarted) {
		return ""
	}
	return probe.Payload
}

// ParseStartedUserID достаёт user_id из bot_started (поле user.user_id).
func ParseStartedUserID(body []byte) int64 {
	var probe struct {
		User struct {
			UserID int64 `json:"user_id"`
		} `json:"user"`
	}
	if err := json.Unmarshal(body, &probe); err != nil {
		return 0
	}
	return probe.User.UserID
}

// SubscribeWebhook регистрирует ровно один webhook в MAX и вызывается при
// старте бота. Это идемпотентная операция в строгом смысле: перед подпиской
// на новый URL бот отписывается от ВСЕХ ранее зарегистрированных URL.
//
// Зачем: quick-tunnel при каждом запуске выдаёт новый URL. Если не чистить
// старые подписки — в MAX копятся мёртвые/дублирующиеся endpoints, и платформа
// начинает слать КАЖДЫЙ апдейт несколько раз (по разу на каждую подписку),
// из-за чего бот отвечает дублями на каждое действие. Чистка — единственный
// правильный путь (а не дедупликация на приёме).
func (a *API) SubscribeWebhook(ctx context.Context, url, secret string) error {
	types := []string{
		string(model.UpdateBotStarted),
		string(model.UpdateBotStopped),
		string(model.UpdateMessageCreated),
		string(model.UpdateMessageCallback),
		string(model.UpdateMessageEdited),
		string(model.UpdateMessageRemoved),
		// Трекинг подписок/отписок на каналы, где бот админ:
		string(model.UpdateUserAdded),
		string(model.UpdateUserRemoved),
	}

	// 1) Чистим все ранее зарегистрированные подписки, чтобы не плодить дублей.
	existing, err := a.Subscriptions.GetSubscriptions(ctx)
	if err != nil {
		slog.Warn("get subscriptions (пропускаем чистку)", "err", err)
	} else {
		for _, s := range existing.Subscriptions {
			if s.URL == "" || s.URL == url {
				continue
			}
			a.rl.wait(ctx)
			if _, uerr := a.Subscriptions.Unsubscribe(ctx, s.URL); uerr != nil {
				slog.Warn("unsubscribe old", "url", s.URL, "err", uerr)
			} else {
				slog.Info("отписан старый webhook", "url", s.URL)
			}
		}
	}

	// 2) Подписываемся на актуальный URL.
	a.rl.wait(ctx)
	res, err := a.Subscriptions.Subscribe(ctx, url, secret, types, "")
	if err != nil {
		return err
	}
	slog.Info("webhook подписан", "url", url, "success", res.Success)
	return nil
}

// SendText — отправка текста пользователю (диалог).
func (a *API) SendText(ctx context.Context, userID int64, text string) error {
	a.rl.wait(ctx)
	_, err := a.Messages.Send(ctx, maxbot.NewMessage().SetUser(userID).SetText(text))
	if err != nil {
		slog.Warn("SendText", "err", err, "user", userID)
	}
	return err
}

// SendTextWithKeyboard — текст + inline-клавиатура пользователю.
func (a *API) SendTextWithKeyboard(ctx context.Context, userID int64, text string, kb *model.Keyboard) error {
	a.rl.wait(ctx)
	msg := maxbot.NewMessage().SetUser(userID).SetText(text)
	if kb != nil {
		msg.AddKeyboard(kb)
	}
	_, err := a.Messages.Send(ctx, msg)
	return err
}

// SendWithAttachments — отправка сообщения с вложениями (медиа + inline-клавиатура)
// в личный диалог с пользователем. Используется для предпросмотра поста розыгрыша.
func (a *API) SendWithAttachments(ctx context.Context, userID int64, text string, attachments []model.Attachment, format model.TextFormat) error {
	a.rl.wait(ctx)
	msg := maxbot.NewMessage().SetUser(userID).SetText(text).SetFormat(format)
	for _, at := range attachments {
		switch at.Type {
		case model.AttachImage:
			if at.Payload.Token != "" {
				msg.AddAttachByToken(at.Payload.Token, model.AttachImage)
			} else if at.Payload.URL != "" {
				msg.AddImageUrl(at.Payload.URL)
			}
		case model.AttachVideo:
			if at.Payload.Token != "" {
				msg.AddAttachByToken(at.Payload.Token, model.AttachVideo)
			}
		case model.AttachInlineKeyboard:
			addInlineKeyboard(msg, at.Payload.Buttons)
		}
	}
	_, err := a.Messages.Send(ctx, msg)
	return err
}

// SendToChat — публикация в канал/чат: текст + attachments (image/video/keyboard).
// attachments передаются как готовые model.Attachment. Inline-клавиатура и
// медиа добавляются через публичные методы maxbot.Message.
func (a *API) SendToChat(ctx context.Context, chatID int64, text string, attachments []model.Attachment, format model.TextFormat) (string, error) {
	a.rl.wait(ctx)
	msg := maxbot.NewMessage().SetChat(chatID).SetText(text).SetFormat(format)
	for _, at := range attachments {
		switch at.Type {
		case model.AttachImage:
			if at.Payload.Token != "" {
				msg.AddAttachByToken(at.Payload.Token, model.AttachImage)
			}
		case model.AttachVideo:
			if at.Payload.Token != "" {
				msg.AddAttachByToken(at.Payload.Token, model.AttachVideo)
			}
		case model.AttachInlineKeyboard:
			// Клавиатура уже собрана в at.Payload.Buttons — пересобираем из матрицы.
			addInlineKeyboard(msg, at.Payload.Buttons)
		}
	}
	res, err := a.Messages.Send(ctx, msg)
	if err != nil {
		return "", err
	}
	return res.Message.Body.Mid, nil
}

// EditChatText — редактирование сообщения в канале (публикация итогов в исходном посте).
func (a *API) EditChatText(ctx context.Context, messageID, text string, attachments []model.Attachment) error {
	a.rl.wait(ctx)
	body := model.NewMessageBody{Text: text}
	body.Attachments = attachments
	_, err := a.Messages.EditMessage(ctx, messageID, body)
	return err
}

// IsSubscriber проверяет, подписан ли пользователь на канал.
// Использует GetMembers с user_ids — один запрос на проверку конкретного юзера.
func (a *API) IsSubscriber(ctx context.Context, chatID, userID int64) (bool, error) {
	a.rl.wait(ctx)
	list, err := a.Chats.GetMembers(ctx, chatID, 0, 0, []int64{userID})
	if err != nil {
		return false, err
	}
	for _, m := range list.Members {
		if m.UserID == userID {
			return true, nil
		}
	}
	return false, nil
}

// IsBotAdminInChat — является ли бот админом в канале.
func (a *API) IsBotAdminInChat(ctx context.Context, chatID int64) (bool, error) {
	a.rl.wait(ctx)
	m, err := a.Chats.GetMembership(ctx, chatID)
	if err != nil {
		return false, err
	}
	return m.IsAdmin, nil
}

// CheckChannelAdmin — проверяет админство бота и заодно возвращает инфу о чате
// (title, link, is_public). Используется при добавлении канала создателем.
func (a *API) CheckChannelAdmin(ctx context.Context, chatID int64) (model.Chat, bool, error) {
	info, err := a.GetChatInfo(ctx, chatID)
	if err != nil {
		return model.Chat{}, false, err
	}
	isAdmin, err := a.IsBotAdminInChat(ctx, chatID)
	if err != nil {
		return info, false, err
	}
	return info, isAdmin, nil
}

// GetChatInfo возвращает информацию о канале/чате.
func (a *API) GetChatInfo(ctx context.Context, chatID int64) (model.Chat, error) {
	a.rl.wait(ctx)
	return a.Chats.GetChat(ctx, chatID)
}

// AnswerCallback — подтверждение callback (снять "часики").
func (a *API) AnswerCallback(ctx context.Context, callbackID, notification string) {
	a.rl.wait(ctx)
	var notif *string
	if notification != "" {
		notif = &notification
	}
	if _, err := a.Messages.AnswerOnCallback(ctx, callbackID, model.CallbackAnswer{Notification: notif}); err != nil {
		slog.Debug("AnswerOnCallback", "err", err)
	}
}

// ---------- хелперы ----------

// addInlineKeyboard пересобирает Keyboard из готовой матрицы кнопок и добавляет в msg.
func addInlineKeyboard(msg *maxbot.Message, buttons [][]*model.Button) {
	kb := model.NewKeyboard()
	for _, row := range buttons {
		r := kb.AddRow()
		for _, b := range row {
			r.AddButton(*b)
		}
	}
	msg.AddKeyboard(kb)
}

// ---------- rate limiter (token bucket) ----------

type rateLimiter struct {
	mu       sync.Mutex
	tokens   int
	max      int
	refillAt time.Time
}

func newRateLimiter(rps int) *rateLimiter {
	if rps < 1 {
		rps = 1
	}
	return &rateLimiter{tokens: rps, max: rps, refillAt: time.Now().Add(time.Second)}
}

// wait блокирует до тех пор, пока не появится свободный токен.
func (r *rateLimiter) wait(ctx context.Context) {
	for {
		r.mu.Lock()
		now := time.Now()
		if now.After(r.refillAt) {
			r.tokens = r.max
			r.refillAt = now.Add(time.Second)
		}
		if r.tokens > 0 {
			r.tokens--
			r.mu.Unlock()
			return
		}
		sleep := time.Until(r.refillAt)
		r.mu.Unlock()

		select {
		case <-ctx.Done():
			return
		case <-time.After(sleep):
		}
	}
}
