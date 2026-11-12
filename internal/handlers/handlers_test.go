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
// 3. FSM: мастер по мануалу Claude-log (10 шагов → scheduled).
// =====================================================================

// wizardHelpers — общие хелперы мастера: быстрый прогон до нужного шага.
// Дефолты StartDraft: кнопка/победители/итоги(завтра 21:00)/старт(сразу)/капча
// предвыбраны — «Далее →» проносит их без вопросов.
type wizardT struct {
	t    *testing.T
	b    *handlers.Bot
	s    *store.Store
	mock *testutil.MockBotAPI
	uid  int64
	ch   store.Channel
}

func newWizard(t *testing.T, uid int64) wizardT {
	t.Helper()
	b, s, mock, cleanup := newBot(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	testutil.EnsureUser(t, s, uid, "creator")
	ch, err := s.AddChannel(ctx, store.Channel{
		OwnerUserID: uid, ChatID: 7001, Title: "Мой канал", Link: "l", IsPublic: true, IsBotAdmin: true,
	})
	require.NoError(t, err)
	return wizardT{t: t, b: b, s: s, mock: mock, uid: uid, ch: ch}
}

func (w wizardT) click(p string) { w.b.Handle(context.Background(), callbackUpdate(w.uid, p)) }
func (w wizardT) send(text string) { w.b.Handle(context.Background(), msgUpdate(w.uid, text)) }
func (w wizardT) step() string {
	gg, e := w.s.GetDraftGiveaway(context.Background(), w.uid)
	require.NoError(w.t, e)
	return gg.DraftStep
}
func (w wizardT) draft() store.Giveaway {
	gg, e := w.s.GetDraftGiveaway(context.Background(), w.uid)
	require.NoError(w.t, e)
	return gg
}

// runTo — прогнать мастера до шага step (включая его показ).
func (w wizardT) runTo(step string) {
	w.click(util.CBCreateGiveaway)
	require.NotEmpty(w.t, w.step())
	for i := 0; w.step() != step; i++ {
		require.Less(w.t, i, 30, "runTo завис: шаг %s не достигается", step)
		switch w.step() {
		case fsm.StepPost:
			if step == fsm.StepPost {
				return
			}
			w.send("Текст поста розыгрыша")
		case fsm.StepChannel:
			w.click(util.CBFSMOption + "ch:" + strconv.FormatInt(w.ch.ID, 10))
			w.click(util.CBFSMNext)
		case fsm.StepName:
			w.click(util.CBFSMOption + "name:0")
			w.click(util.CBFSMNext)
		default:
			w.click(util.CBFSMNext)
		}
	}
}

// TestFSM_FullCreateFlow_Scheduled — полный путь по мануалу: пост → канал →
// название (авто-вариант) → подписки (Далее) → кнопка/победители/итоги/старт
// (дефолты, Далее-Далее) → доп. настройки → проверка → 🚀 Опубликовать.
func TestFSM_FullCreateFlow_Scheduled(t *testing.T) {
	w := newWizard(t, 5000)
	ctx := context.Background()

	// Старт создания → шаг 1 «Пост».
	w.click(util.CBCreateGiveaway)
	require.Equal(w.t, fsm.StepPost, w.step())
	gid := w.draft().ID

	// 1) Пост текстом → сразу шаг 2.
	w.send("Пост розыгрыша")
	require.Equal(t, fsm.StepChannel, w.step())

	// 2) Канал публикации: выбор морфит шаг («✓»), Далее → шаг 3.
	w.click(util.CBFSMOption + "ch:" + strconv.FormatInt(w.ch.ID, 10))
	require.Equal(t, fsm.StepChannel, w.step(), "выбор канала не переводит шаг")
	w.click(util.CBFSMNext)
	require.Equal(t, fsm.StepName, w.step())

	// 3) Название: авто-вариант + Далее.
	w.click(util.CBFSMOption + "name:0")
	g := w.draft()
	require.NotEmpty(t, g.Name, "авто-вариант названия должен сохраниться")
	w.click(util.CBFSMNext)
	require.Equal(t, fsm.StepSubs, w.step())

	// 4) Подписки: канал публикации предвыбран 🔒, «Далее · выбрано 1».
	w.click(util.CBFSMNext)
	require.Equal(t, fsm.StepButton, w.step())

	// 5-8) Кнопка/победители/итоги/старт — дефолты предвыбраны.
	w.click(util.CBFSMNext)
	require.Equal(t, fsm.StepWinners, w.step())
	w.click(util.CBFSMNext)
	require.Equal(t, fsm.StepFinish, w.step())
	w.click(util.CBFSMNext)
	require.Equal(t, fsm.StepStart, w.step())
	w.click(util.CBFSMNext)
	require.Equal(t, fsm.StepExtras, w.step())

	// 9) Доп. настройки: капча включена по дефолту (мануал) → Далее.
	g = w.draft()
	assert.Equal(t, store.CaptchaAll, g.CaptchaMode, "капча включена по умолчанию")
	w.click(util.CBFSMNext)
	require.Equal(t, fsm.StepCheck, w.step())

	// Экран проверки: превью поста + сводка.
	sawSummary := false
	for _, r := range w.mock.SnapshotSends() {
		if strings.Contains(r.Text, "Информация о розыгрыше") {
			sawSummary = true
		}
	}
	assert.True(t, sawSummary, "сводка на экране проверки")

	// 10) 🚀 Опубликовать → scheduled (старт «сразу»: publish_at=now).
	w.click(util.CBFSMConfirm)
	got, err := w.s.GetGiveawayAny(ctx, gid)
	require.NoError(t, err)
	assert.Equal(t, store.StatusScheduled, got.Status)
	assert.Equal(t, 1, got.WinnersCount, "дефолт: 1 победитель")
	assert.Equal(t, "Участвовать", got.ButtonText)
	assert.Equal(t, []int64{w.ch.ID}, got.PublishChannelIDs)
	assert.Equal(t, []int64{w.ch.ID}, got.SubscribeChannelIDs, "канал публикации предвыбран в подписках")
	assert.True(t, got.PublishNow, "дефолт: старт сразу")
	assert.Equal(t, "time", got.FinishMode)
	assert.NotNil(t, got.FinishAt, "дефолт: итоги завтра 21:00")
	assert.Equal(t, "Пост розыгрыша", got.PostText)
}

// TestFSM_FinishByCount — «👥 По числу участников»: цель вводится текстом на
// том же шаге, минимум 10 (мануал).
func TestFSM_FinishByCount(t *testing.T) {
	w := newWizard(t, 5006)
	w.runTo(fsm.StepFinish)

	w.click(util.CBFSMOption + "fin_count")
	g := w.draft()
	assert.Equal(t, "count", g.FinishMode)
	assert.Equal(t, 100, g.FinishParticipantsCount, "дефолт цели при включении режима")
	assert.Equal(t, fsm.StepFinish, w.step(), "режим меняется на месте, без перехода")

	// Слишком мало — ошибка на месте (❌-морфинг), шаг не меняется.
	w.send("5")
	assert.Equal(t, fsm.StepFinish, w.step())

	// Валидное число → сразу шаг 8.
	w.send("150")
	g = w.draft()
	assert.Equal(t, 150, g.FinishParticipantsCount)
	assert.Equal(t, fsm.StepStart, w.step())
}

// TestFSM_Extras_TogglesAndMessage — доп. настройки: переключатели морфят шаг,
// ✉️ открывает подэкран, текст сохраняется и возвращает к настройкам.
func TestFSM_Extras_TogglesAndMessage(t *testing.T) {
	w := newWizard(t, 5007)
	w.runTo(fsm.StepExtras)

	// Капча: вкл → выкл.
	w.click(util.CBFSMOption + "cap")
	assert.Equal(t, store.CaptchaNone, w.draft().CaptchaMode)
	// Друзья: выкл → вкл (лимит 10 по дефолту).
	w.click(util.CBFSMOption + "fr")
	g := w.draft()
	assert.True(t, g.ReferralEnabled)
	// Лимит 3.
	w.click(util.CBFSMOption + "frlim:3")
	assert.Equal(t, 3, w.draft().ReferralLimit)
	assert.Equal(t, fsm.StepExtras, w.step(), "переключатели не переводят шаг")

	// ✉️ Сообщение победителю: подэкран → текст → назад к настройкам.
	w.click(util.CBFSMOption + "msg")
	assert.Equal(t, fsm.StepExtras, w.step())
	w.send("Пишите в личку за призом")
	g = w.draft()
	assert.Equal(t, "Пишите в личку за призом", g.WinnersMessage)
	assert.Equal(t, fsm.StepExtras, w.step())

	// Место итогов: отдельным постом → подэкран текста итогов.
	w.click(util.CBFSMOption + "resloc")
	assert.False(t, w.draft().ResultsInOriginalPost)
	w.send("🏁 Наши победители")
	assert.Equal(t, "🏁 Наши победители", w.draft().ResultsPostText)
}

// TestFSM_CancelConfirm_KeepsDraft — «✕ Отменить» показывает подтверждение,
// «Да, выйти» сохраняет именованный черновик.
func TestFSM_CancelConfirm_KeepsDraft(t *testing.T) {
	w := newWizard(t, 5002)
	w.click(util.CBCreateGiveaway)
	require.NoError(t, w.s.UpdateGiveawayField(context.Background(), w.draft().ID, "name", "Мой черновик"))

	// ✕ Отменить → экран подтверждения (морфинг, без нового сообщения).
	w.click(util.CBFSMCancel)
	sawConfirm := false
	for _, r := range w.mock.SnapshotSends() {
		if strings.Contains(r.Text, "Остановить создание") {
			sawConfirm = true
		}
	}
	assert.True(t, sawConfirm, "должен появиться экран подтверждения отмены")
	assert.Equal(t, store.StatusDraft, w.draft().Status, "после ✕ черновик ещё жив")

	// «Да, выйти» — черновик сохранён.
	w.click(util.CBFSMExitYes)
	got, err := w.s.GetGiveawayAny(context.Background(), w.draft().ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusDraft, got.Status)
	assert.NotEmpty(t, got.DraftStep)
}

// TestFSM_BackEditFromCheck — правка пункта с экрана проверки: fsm_edit
// ведёт к шагу, «Далее» возвращает на проверку (return_to).
func TestFSM_BackEditFromCheck(t *testing.T) {
	w := newWizard(t, 5008)
	w.runTo(fsm.StepCheck)

	// Правка названия (шаг 3).
	w.click(util.CBFSMEdit + fsm.StepName)
	assert.Equal(t, fsm.StepName, w.step())
	w.send("Новое имя")
	assert.Equal(t, fsm.StepCheck, w.step(), "после правки — назад на проверку")
	assert.Equal(t, "Новое имя", w.draft().Name)
}

// TestFSM_HintIsNotification — 💡 Справка отвечает УВЕДОМЛЕНИЕМ (≤150
// символов), не новым сообщением (мануал §3).
func TestFSM_HintIsNotification(t *testing.T) {
	w := newWizard(t, 5009)
	w.click(util.CBCreateGiveaway)
	sendsBefore := len(w.mock.SnapshotSends())

	w.click(util.CBFSMHint)

	assert.Equal(t, sendsBefore, len(w.mock.SnapshotSends()), "нового сообщения быть не должно")
	require.NotEmpty(t, w.mock.CallbackNotifs, "подсказка должна прийти уведомлением")
	assert.Contains(t, w.mock.CallbackNotifs[len(w.mock.CallbackNotifs)-1], "символ")
}

// TestFSM_ValidationFails_NoSchedule — 🚀 с незаполненным постом не
// публикует: уведомление об ошибке, статус draft.
func TestFSM_ValidationFails_NoSchedule(t *testing.T) {
	w := newWizard(t, 5003)
	w.click(util.CBCreateGiveaway)
	g := w.draft()
	require.NoError(t, w.s.SetDraftStep(context.Background(), g.ID, fsm.StepCheck))

	w.click(util.CBFSMConfirm)

	got, err := w.s.GetGiveawayAny(context.Background(), g.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusDraft, got.Status, "не должен опубликоваться без обязательных полей")
	require.NotEmpty(t, w.mock.CallbackNotifs, "ошибка валидации — уведомлением")
	assert.Contains(t, w.mock.CallbackNotifs[len(w.mock.CallbackNotifs)-1], "пост")
}

// TestFSM_Confirm_ScheduledByTime — старт «по времени»: подтверждение →
// scheduled с заданным publish_at.
func TestFSM_Confirm_ScheduledByTime(t *testing.T) {
	w := newWizard(t, 5004)
	ctx := context.Background()
	w.click(util.CBCreateGiveaway)
	g := w.draft()
	pub := time.Now().Add(2 * time.Hour)
	fin := time.Now().Add(26 * time.Hour)
	for field, val := range map[string]any{
		"post_text": "Пост", "name": "К", "button_text": "Участвовать",
		"publish_channel_ids": []int64{w.ch.ID}, "publish_at": pub, "finish_at": fin,
		"winners_count": 1, "publish_now": false,
	} {
		require.NoError(t, w.s.UpdateGiveawayField(ctx, g.ID, field, val))
	}
	require.NoError(t, w.s.SetDraftStep(ctx, g.ID, fsm.StepCheck))

	w.click(util.CBFSMConfirm)

	got, err := w.s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusScheduled, got.Status, "подтверждение → scheduled (не active)")
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
		if r.UserID == 6000 && r.HasKb && strings.Contains(r.Text, "RandoMaster") {
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

	// /menu → SendTextWithKeyboard (inline: reply-keyboard в MAX ботам недоступен).
	b.Handle(ctx, msgUpdate(7000, "/menu"))
	require.Greater(t, sendsByMethod(mock, "SendTextWithKeyboard"), 0)

	// Поддержка → SendTextWithKeyboard (инлайн внутри разделов).
	before := sendsByMethod(mock, "SendTextWithKeyboard")
	b.Handle(ctx, callbackUpdate(7000, util.CBSupport))
	assert.Greater(t, sendsByMethod(mock, "SendTextWithKeyboard"), before)
}
