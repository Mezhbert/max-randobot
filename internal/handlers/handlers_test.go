package handlers_test

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maxbot/giveawaybot/internal/dedup"
	"github.com/maxbot/giveawaybot/internal/fsm"
	"github.com/maxbot/giveawaybot/internal/handlers"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/testutil"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// ---------- конструкторы апдейтов ----------

// msgUpdate — текстовое сообщение от пользователя.
// Каждый вызов получает уникальный MessageID (как в реальном MAX), чтобы
// дедупликатор не воспринимал разные сообщения одного юзера как дубли.
var msgSeq int64

func msgUpdate(userID int64, text string) model.Update {
	msgSeq++
	return model.Update{
		UpdateType: model.UpdateMessageCreated,
		UserID:     userID,
		MessageID:  "mid.test." + strconv.FormatInt(msgSeq, 10),
		User:       &model.User{UserID: userID, FirstName: "tester"},
		Message: &model.MessageUpdate{
			Body: model.MessageBody{Text: text},
		},
	}
}

// callbackUpdate — нажатие inline-кнопки с payload.
func callbackUpdate(userID int64, payload string) model.Update {
	return model.Update{
		UpdateType: model.UpdateMessageCallback,
		UserID:     userID,
		User:       &model.User{UserID: userID, FirstName: "tester"},
		Callback: &model.Callback{
			CallbackID: "cb_" + payload + "_" + strconv.FormatInt(time.Now().UnixNano(), 10),
			Payload:    payload,
			User:       model.User{UserID: userID, FirstName: "tester"},
		},
	}
}

// callbackUpdateID — callback с фиксированным callback_id (для теста дедупа).
func callbackUpdateID(userID int64, payload, callbackID string) model.Update {
	return model.Update{
		UpdateType: model.UpdateMessageCallback,
		UserID:     userID,
		User:       &model.User{UserID: userID, FirstName: "tester"},
		Callback: &model.Callback{
			CallbackID: callbackID,
			Payload:    payload,
			User:       model.User{UserID: userID, FirstName: "tester"},
		},
	}
}

// newBot — поднимает test-PG + mock API + Bot с дедупом по умолчанию.
func newBot(t *testing.T) (*handlers.Bot, *store.Store, *testutil.MockBotAPI, func()) {
	t.Helper()
	s, cleanup := testutil.NewTestStore(t)
	mock := testutil.NewMock()
	b := handlers.New(mock, s, nil)
	d := dedup.New(dedup.DefaultTTL, 0)
	b.SetDedup(d)
	return b, s, mock, cleanup
}

// sendsToUser — сколько сообщений отправлено конкретному пользователю.
func sendsToUser(m *testutil.MockBotAPI, userID int64) int {
	n := 0
	for _, r := range m.SnapshotSends() {
		if r.UserID == userID {
			n++
		}
	}
	return n
}

// sendsByMethod — сколько вызовов метода BotAPI сделано.
func sendsByMethod(m *testutil.MockBotAPI, method string) int {
	n := 0
	for _, r := range m.SnapshotSends() {
		if r.Method == method {
			n++
		}
	}
	return n
}

// =====================================================================
// 1. ДЕДУП: одинаковый апдейт (ретраи webhook) → один ответ.
// =====================================================================

// TestDedup_DuplicateCallback_OneResponse — MAX ретраит один callback → бот
// отвечает один раз, а не N. Это ключевой фикс «дабл-меню».
func TestDedup_DuplicateCallback_OneResponse(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	testutil.EnsureUser(t, s, 1000, "tester")

	// Один и тот же callback_id (тот же физический апдейт) приходит 3 раза.
	const cbID = "cb_dup_1"
	u := callbackUpdateID(1000, util.CBMainMenu, cbID)

	before := sendsToUser(mock, 1000)
	for i := 0; i < 3; i++ {
		b.Handle(ctx, u)
	}
	after := sendsToUser(mock, 1000)

	// Должно прибавиться ровно одно сообщение меню.
	assert.Equal(t, before+1, after,
		"дублированный апдейт должен дать один ответ, а не %d (было %d, стало %d)",
		after-before, before, after)
}

// TestDedup_DifferentCallbacks_AllProcessed — разные callback_id → все обрабатываются.
func TestDedup_DifferentCallbacks_AllProcessed(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	testutil.EnsureUser(t, s, 1001, "tester")

	for i := 0; i < 3; i++ {
		b.Handle(ctx, callbackUpdateID(1001, util.CBMainMenu, "cb_unique_"+strconv.Itoa(i)))
	}

	// Три разных апдейта → три ответа.
	assert.Equal(t, 3, sendsToUser(mock, 1001),
		"разные апдейты не должны дедуплиться")
}

// TestDedup_MessageByText_DedupsByMessageID — для message_created ключом служит
// MessageID. Ретраи (тот же mid) отбрасываются.
func TestDedup_MessageByText_DedupsByMessageID(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	testutil.EnsureUser(t, s, 1002, "tester")

	u := msgUpdate(1002, "/menu")
	u.MessageID = "mid.dup.1" // MAX присылает уникальный mid на каждое сообщение

	before := sendsToUser(mock, 1002)
	for i := 0; i < 3; i++ {
		b.Handle(ctx, u)
	}
	after := sendsToUser(mock, 1002)

	assert.Equal(t, before+1, after, "сообщение с тем же mid должно дедуплиться")
}

// =====================================================================
// 2. PARTICIPATE: кнопка «Участвовать» с проверкой подписки.
// =====================================================================

// setupActiveGiveawayWithChannel — активный розыгрыш с каналом для подписки.
func setupActiveGiveawayWithChannel(t *testing.T, s *store.Store, ownerID int64) (store.Giveaway, store.Channel) {
	t.Helper()
	ctx := context.Background()
	testutil.EnsureUser(t, s, ownerID, "")

	ch, err := s.AddChannel(ctx, store.Channel{
		OwnerUserID: ownerID, ChatID: 3001, Title: "Подпишись", Link: "l", IsPublic: true, IsBotAdmin: true,
	})
	require.NoError(t, err)

	// Giveaway в статусе active с каналом для подписки.
	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(time.Hour)
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO giveaways (owner_user_id, name, post_text, button_text,
		  subscribe_channel_ids, publish_channel_ids, status, publish_at, finish_at, winners_count)
		VALUES ($1,'Конкурс','Текст','Участвовать',
		  ARRAY[$2]::bigint[], ARRAY[$2]::bigint[], 'active', $3, $4, 1)
	`, ownerID, ch.ID, past, future)
	require.NoError(t, err)

	items, err := s.ActiveFinished(ctx, time.Now().UTC().Add(2*time.Hour))
	_ = items
	// ActiveFinished ищет по finish_at<=now, нам же нужен просто активный —
	// достаём напрямую по владельцу.
	gws, err := s.ListGiveaways(ctx, ownerID, store.StatusActive)
	require.NoError(t, err)
	require.NotEmpty(t, gws)
	return gws[0], ch
}

// TestParticipate_Subscribed_Registers — подписанный пользователь участвует.
func TestParticipate_Subscribed_Registers(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	g, ch := setupActiveGiveawayWithChannel(t, s, 2000)

	const participant = 7777
	testutil.EnsureUser(t, s, participant, "winner")
	// Подписан на канал.
	mock.IsSubscriberMap = map[int64]map[int64]bool{
		ch.ChatID: {participant: true},
	}

	b.Handle(ctx, callbackUpdate(participant, util.CBParticipate+strconv.FormatInt(g.ID, 10)))

	// Участник получил ровно одно поздравление.
	assert.Equal(t, 1, sendsToUser(mock, participant),
		"участник должен получить одно сообщение об участии")

	// Записан в participants.
	parts, err := s.ListParticipantsForDraw(ctx, g.ID)
	require.NoError(t, err)
	require.Len(t, parts, 1)
	assert.Equal(t, int64(participant), parts[0].UserID)
}

// TestParticipate_NotSubscribed_Rejected — неподписанный пользователь
// получает просьбу подписаться, в participants НЕ записывается.
func TestParticipate_NotSubscribed_Rejected(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	g, ch := setupActiveGiveawayWithChannel(t, s, 2001)

	const participant = 8888
	testutil.EnsureUser(t, s, participant, "nosub")
	// НЕ подписан.
	mock.IsSubscriberMap = map[int64]map[int64]bool{
		ch.ChatID: {participant: false},
	}

	b.Handle(ctx, callbackUpdate(participant, util.CBParticipate+strconv.FormatInt(g.ID, 10)))

	// Получил сообщение с просьбой подписаться.
	msgs := 0
	for _, r := range mock.SnapshotSends() {
		if r.UserID == participant && strings.Contains(r.Text, "подпишитесь") {
			msgs++
		}
	}
	assert.Equal(t, 1, msgs, "должна быть просьба подписаться")

	// НЕ записан в participants.
	parts, err := s.ListParticipantsForDraw(ctx, g.ID)
	require.NoError(t, err)
	assert.Empty(t, parts, "неподписанный не должен стать участником")
}

// TestParticipate_Idempotent_DoubleClick — повторное нажатие «Участвовать»
// не создаёт дубль участника и не шлёт второе поздравление.
func TestParticipate_Idempotent_DoubleClick(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	g, ch := setupActiveGiveawayWithChannel(t, s, 2002)

	const participant = 9999
	testutil.EnsureUser(t, s, participant, "dbl")
	mock.IsSubscriberMap = map[int64]map[int64]bool{
		ch.ChatID: {participant: true},
	}

	payload := util.CBParticipate + strconv.FormatInt(g.ID, 10)
	// Два разных callback_id (реальные клики) — оба должны обработаться,
	// но AddParticipant идемпотентен.
	b.Handle(ctx, callbackUpdate(participant, payload))
	b.Handle(ctx, callbackUpdate(participant, payload))

	// Участник один.
	parts, err := s.ListParticipantsForDraw(ctx, g.ID)
	require.NoError(t, err)
	require.Len(t, parts, 1, "должен быть ровно один участник")
	assert.Equal(t, int64(participant), parts[0].UserID)

	// Второй клик → alert «Вы уже участвуете» (через AnswerCallback), не второе
	// поздравление и не дубль SendText. Проверяем: поздравление ровно одно.
	congrats := 0
	for _, r := range mock.SnapshotSends() {
		if r.UserID == participant && strings.Contains(r.Text, "Вы участвуете") {
			congrats++
		}
	}
	assert.Equal(t, 1, congrats, "поздравление — один раз")

	// Бонус: проверяем, что captcha не требовалась (default captcha_mode=none),
	// поэтому участие зарегистрировано сразу, без image-капчи.
	images := 0
	for _, r := range mock.SnapshotSends() {
		if r.Method == "SendImage" {
			images++
		}
	}
	assert.Equal(t, 0, images, "без капчи — картинка не отправляется")
}

// =====================================================================
// 3. FSM: полный флоу создания розыгрыша (13 шагов → scheduled).
// =====================================================================

// TestFSM_FullCreateFlow_Scheduled — полный путь создания: текст поста →
// skip медиа → название → skip кнопки → skip sub-каналы → выбор pub-канала →
// даты → победители → skip сообщение → результаты в исходном посте (прыгает
// results_text) → skip рефералы → skip капча → skip продвижение → Принять →
// статус scheduled.
func TestFSM_FullCreateFlow_Scheduled(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	const owner = 5000
	testutil.EnsureUser(t, s, owner, "creator")

	// Канал для публикации (бот админ).
	ch, err := s.AddChannel(ctx, store.Channel{
		OwnerUserID: owner, ChatID: 7001, Title: "Мой канал", Link: "l", IsPublic: true, IsBotAdmin: true,
	})
	require.NoError(t, err)

	// Старт создания.
	b.Handle(ctx, callbackUpdate(owner, util.CBCreateGiveaway))
	// Должны быть на шаге name (теперь первый шаг).
	g, err := s.GetDraftGiveaway(ctx, owner)
	require.NoError(t, err)
	require.Equal(t, fsm.StepName, g.DraftStep)
	gid := g.ID

	// Хелпер: отправить текст для текущего шага.
	sendText := func(text string) {
		t.Helper()
		b.Handle(ctx, msgUpdate(owner, text))
	}
	// Хелпер: нажать callback.
	click := func(payload string) {
		t.Helper()
		b.Handle(ctx, callbackUpdate(owner, payload))
	}
	// Хелпер: текущий шаг черновика.
	curStep := func() string {
		t.Helper()
		gg, e := s.GetDraftGiveaway(ctx, owner)
		require.NoError(t, e)
		return gg.DraftStep
	}

	// 1) Название (теперь первый шаг).
	sendText("Супер-конкурс")
	require.Equal(t, fsm.StepPostText, curStep())

	// 2) Текст поста.
	sendText("Пост розыгрыша")
	require.Equal(t, fsm.StepMedia, curStep())

	// 3) Медиа — пропустить.
	click(util.CBFSMSkip)
	require.Equal(t, fsm.StepButtonText, curStep())

	// 4) Текст кнопки — пропустить (дефолт «Участвовать»).
	click(util.CBFSMSkip)
	require.Equal(t, fsm.StepSubChannels, curStep())

	// 5) Каналы для подписки — «Далее» без выбора.
	click(util.CBFSMOption + "next")
	require.Equal(t, fsm.StepPubChannels, curStep())

	// 6) Канал публикации — выбираем тогглом, затем «Далее».
	click("ch_toggle_pub:" + strconv.FormatInt(ch.ID, 10))
	click(util.CBFSMOption + "next")
	require.Equal(t, fsm.StepPublishAt, curStep())

	// 7) Дата публикации — в будущем. Форматируем по МСК (как видит юзер),
	// чтобы ParseMoscowTime дал корректное UTC-значение в будущем.
	pubAt := time.Now().Add(48 * time.Hour)
	sendText(util.FormatMoscowTime(pubAt))
	require.Equal(t, fsm.StepFinishAt, curStep())

	// 8) Дата итогов — ещё дальше.
	finAt := time.Now().Add(7 * 24 * time.Hour)
	sendText(util.FormatMoscowTime(finAt))
	require.Equal(t, fsm.StepWinners, curStep())

	// 9) Победители.
	sendText("3")
	require.Equal(t, fsm.StepWinnersMsg, curStep())

	// 10) Сообщение победителям — пропустить.
	click(util.CBFSMSkip)
	require.Equal(t, fsm.StepResultsLoc, curStep())

	// 11) Итоги — «В исходном посте» → должен перепрыгнуть results_text.
	click(util.CBFSMOption + "results_original")
	require.Equal(t, fsm.StepCaptcha, curStep(),
		"results_original должен перепрыгнуть шаг results_text к captcha")

	// 12) Капча — «Нет».
	click(util.CBFSMOption + "captcha_none")
	require.Equal(t, fsm.StepPreview, curStep())

	// Принять.
	click(util.CBFSMConfirm)

	// Статус — scheduled (publish_at в будущем).
	got, err := s.GetGiveawayAny(ctx, gid)
	require.NoError(t, err)
	assert.Equal(t, store.StatusScheduled, got.Status, "после подтверждения — scheduled")
	assert.Equal(t, 3, got.WinnersCount)
	assert.Len(t, got.PublishChannelIDs, 1)
	assert.Equal(t, ch.ID, got.PublishChannelIDs[0])
	assert.True(t, got.ResultsInOriginalPost, "results_in_original_post должен быть TRUE")
	// post_text сохранён.
	assert.Equal(t, "Пост розыгрыша", got.PostText)
	assert.Equal(t, "Супер-конкурс", got.Name)
}

// TestFSM_ResultsSeparate_AsksForResultsText — выбор «отдельным постом»
// НЕ перепрыгивает шаг results_text (там запрашивается текст итогов).
func TestFSM_ResultsSeparate_AsksForResultsText(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	const owner = 5001
	testutil.EnsureUser(t, s, owner, "creator")

	b.Handle(ctx, callbackUpdate(owner, util.CBCreateGiveaway))
	g, err := s.GetDraftGiveaway(ctx, owner)
	require.NoError(t, err)

	// Переводим черновик сразу на шаг results_loc.
	require.NoError(t, s.SetDraftStep(ctx, g.ID, fsm.StepResultsLoc))
	g.DraftStep = fsm.StepResultsLoc

	// Выбираем «отдельным постом».
	b.Handle(ctx, callbackUpdate(owner, util.CBFSMOption+"results_separate"))

	gg, err := s.GetDraftGiveaway(ctx, owner)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepResultsText, gg.DraftStep,
		"results_separate должен привести на шаг results_text")
	assert.False(t, gg.ResultsInOriginalPost, "results_in_original_post должен быть FALSE")
}

// TestFSM_Cancel_KeepsDraft — кнопка «Отменить» сохраняет именованный черновик
// (доступен позже через «Мои розыгрыши»), сбрасывает шаг. Безымянный — удаляется.
func TestFSM_Cancel_KeepsDraft(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	const owner = 5002
	testutil.EnsureUser(t, s, owner, "creator")

	b.Handle(ctx, callbackUpdate(owner, util.CBCreateGiveaway))
	g, err := s.GetDraftGiveaway(ctx, owner)
	require.NoError(t, err)
	require.NotEmpty(t, g.DraftStep)

	// Даём черновику имя — тогда Cancel сохранит его.
	require.NoError(t, s.UpdateGiveawayField(ctx, g.ID, "name", "Мой черновик"))

	// Отмена.
	b.Handle(ctx, callbackUpdate(owner, util.CBFSMCancel))

	// Статус — всё ещё draft, draft_step СОХРАНЁН (чтобы «Продолжить» началось с того же шага).
	got, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusDraft, got.Status)
	assert.NotEmpty(t, got.DraftStep, "после отмены draft_step сохраняется для продолжения")

	// Получил сообщение об отмене.
	found := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == owner && strings.Contains(r.Text, "отменено") {
			found = true
		}
	}
	assert.True(t, found, "должно быть сообщение об отмене")
}

// TestFSM_ValidationFails_NoSchedule — подтверждение без обязательных полей
// НЕ переводит в scheduled, а выдаёт ошибку валидации.
func TestFSM_ValidationFails_NoSchedule(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	const owner = 5003
	testutil.EnsureUser(t, s, owner, "creator")

	b.Handle(ctx, callbackUpdate(owner, util.CBCreateGiveaway))
	g, err := s.GetDraftGiveaway(ctx, owner)
	require.NoError(t, err)

	// Сразу переводим на preview без заполнения обязательных полей.
	require.NoError(t, s.SetDraftStep(ctx, g.ID, fsm.StepPreview))

	// Принять → должна сработать валидация (нет текста поста).
	b.Handle(ctx, callbackUpdate(owner, util.CBFSMConfirm))

	got, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusDraft, got.Status, "не должен перейти в scheduled без обязательных полей")

	// Пользователь получил сообщение об ошибке валидации.
	found := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == owner && strings.Contains(r.Text, "⚠️") {
			found = true
		}
	}
	assert.True(t, found, "должна быть ошибка валидации")
}

// TestFSM_Confirm_AlwaysScheduled — подтверждение всегда переводит в scheduled
// (не active!), даже если publish_at «прямо сейчас». Scheduler сам опубликует.
func TestFSM_Confirm_AlwaysScheduled(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	const owner = 5004
	testutil.EnsureUser(t, s, owner, "creator")

	ch, err := s.AddChannel(ctx, store.Channel{
		OwnerUserID: owner, ChatID: 7002, Title: "Канал", Link: "l", IsPublic: true, IsBotAdmin: true,
	})
	require.NoError(t, err)

	b.Handle(ctx, callbackUpdate(owner, util.CBCreateGiveaway))
	g, err := s.GetDraftGiveaway(ctx, owner)
	require.NoError(t, err)

	// Заполняем все обязательные поля, publish_at в будущем.
	pub := time.Now().Add(2 * time.Hour)
	fin := time.Now().Add(3 * time.Hour)
	require.NoError(t, s.UpdateGiveawayField(ctx, g.ID, "post_text", "Пост"))
	require.NoError(t, s.UpdateGiveawayField(ctx, g.ID, "name", "К"))
	require.NoError(t, s.UpdateGiveawayField(ctx, g.ID, "button_text", "Участвовать"))
	require.NoError(t, s.UpdateGiveawayField(ctx, g.ID, "publish_channel_ids", []int64{ch.ID}))
	require.NoError(t, s.UpdateGiveawayField(ctx, g.ID, "publish_at", pub))
	require.NoError(t, s.UpdateGiveawayField(ctx, g.ID, "finish_at", fin))
	require.NoError(t, s.UpdateGiveawayField(ctx, g.ID, "winners_count", 1))
	require.NoError(t, s.SetDraftStep(ctx, g.ID, fsm.StepPreview))

	// Принять.
	b.Handle(ctx, callbackUpdate(owner, util.CBFSMConfirm))

	got, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusScheduled, got.Status, "подтверждение → всегда scheduled (не active)")
}

// =====================================================================
// 4. /start — главное меню (без дублей благодаря дедупу).
// =====================================================================

// TestStart_SendsMainMenu — /start присылает главное меню.
func TestStart_SendsMainMenu(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	testutil.EnsureUser(t, s, 6000, "starter")

	// /start (msgUpdate уже генерирует уникальный mid, как реальный запуск).
	b.Handle(ctx, msgUpdate(6000, "/start"))

	// Должно быть отправлено хотя бы одно сообщение с клавиатурой.
	hasMenu := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == 6000 && r.HasKb && strings.Contains(r.Text, "меню") {
			hasMenu = true
		}
	}
	assert.True(t, hasMenu, "должно прийти главное меню")
	assert.Equal(t, 1, sendsToUser(mock, 6000), "одно сообщение меню на /start")
}

// TestBotStarted_SendsMainMenu — update bot_started (кнопка старта в MAX) даёт меню.
func TestBotStarted_SendsMainMenu(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	const uid = 6001
	testutil.EnsureUser(t, s, uid, "started")

	u := model.Update{
		UpdateType: model.UpdateBotStarted,
		UserID:     uid,
		User:       &model.User{UserID: uid, FirstName: "new"},
	}
	b.Handle(ctx, u)

	assert.Equal(t, 1, sendsToUser(mock, uid), "bot_started → одно меню")
}

// =====================================================================
// 5. Sanity: упоминание методов (защита от рефактора, ломающего мок).
// =====================================================================

// TestMockAPI_CoversAllUsedMethods — компиляция теста гарантирует, что
// MockBotAPI реализует BotAPI (var _ maxapi.BotAPI в testutil). Дополнительно
// проверяем, что ключевые методы вызываются через Handle.
func TestMockAPI_CoversAllUsedMethods(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	testutil.EnsureUser(t, s, 7000, "tester")

	// /menu → SendTextWithKeyboard.
	b.Handle(ctx, msgUpdate(7000, "/menu"))
	require.Greater(t, sendsByMethod(mock, "SendTextWithKeyboard"), 0)

	// Поддержка → SendTextWithKeyboard.
	before := sendsByMethod(mock, "SendTextWithKeyboard")
	b.Handle(ctx, callbackUpdate(7000, util.CBSupport))
	assert.Greater(t, sendsByMethod(mock, "SendTextWithKeyboard"), before)
}
