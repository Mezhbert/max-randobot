package handlers_test

// Тесты большой итерации: reply-keyboard, каналы (refresh/ссылка/ранний чек),
// условная навигация FSM, редактура (медиа/каналы), тихое участие,
// участники (удаление), UTM без канала, экранирование итогов.

import (
	"context"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maxbot/giveawaybot/internal/fsm"
	"github.com/maxbot/giveawaybot/internal/scheduler"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/testutil"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// ---------- Меню ----------

// TestMenu_InlineAlways — /menu шлёт inline-клавиатуру (reply-keyboard в MAX
// ботам недоступен: платформа выкидывает attachment — проверено живым API).
func TestMenu_InlineAlways(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6001
	testutil.EnsureUser(t, s, uid, "tester")

	// /menu → SendTextWithKeyboard с инлайн-кнопками.
	b.Handle(ctx, msgUpdate(uid, "/menu"))
	assert.Greater(t, sendsByMethod(mock, "SendTextWithKeyboard"), 0,
		"меню должно отправляться inline-клавиатурой")

	// Никаких reply-методов в API больше нет — проверяем, что не падает.
	found := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Главное меню") {
			found = true
		}
	}
	assert.True(t, found)
}

// TestMenu_TextFallback — подпись кнопки меню, отправленная текстом,
// маршрутизируется (fallback для сообщений от кнопок-как-текст).
func TestMenu_TextFallback(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6002
	testutil.EnsureUser(t, s, uid, "tester")

	before := sendsToUser(mock, uid)
	b.Handle(ctx, msgUpdate(uid, "📊 Аналитика"))
	assert.Equal(t, before+1, sendsToUser(mock, uid), "текст подписи кнопки меню должен открывать раздел")
}

// ---------- Каналы ----------

// TestChannels_Refresh_RemovesLost — 🔄 удаляет каналы, где бот больше не админ.
func TestChannels_Refresh_RemovesLost(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6101
	testutil.EnsureUser(t, s, uid, "owner")

	_, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8101, Title: "Живой", IsBotAdmin: true})
	require.NoError(t, err)
	_, err = s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8102, Title: "Мёртвый", IsBotAdmin: true})
	require.NoError(t, err)

	// Мок: бот больше не админ в 8102 (напр. админ канала его убрал).
	mock.IsBotAdminMap = map[int64]bool{8101: true, 8102: false}

	b.Handle(ctx, callbackUpdate(uid, util.CBChannelRefresh))

	chs, err := s.ListChannels(ctx, uid)
	require.NoError(t, err)
	ids := map[int64]bool{}
	for _, c := range chs {
		ids[c.ChatID] = true
	}
	assert.True(t, ids[8101], "живой канал должен остаться")
	assert.False(t, ids[8102], "канал без админства должен удалиться при обновлении")
}

// TestChannels_BotRemovedUpdate_FlagsChannel — webhook bot_removed снимает
// флаг админства глобально по chat_id.
func TestChannels_BotRemovedUpdate_FlagsChannel(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6102
	testutil.EnsureUser(t, s, uid, "owner")

	_, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8201, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	b.Handle(ctx, model.Update{UpdateType: model.UpdateBotRemoved, UserID: uid, ChatID: 8201})
	ch, err := s.GetChannelByChat(ctx, uid, 8201)
	require.NoError(t, err)
	assert.False(t, ch.IsBotAdmin, "bot_removed должен снять флаг is_bot_admin")

	b.Handle(ctx, model.Update{UpdateType: model.UpdateBotAdded, UserID: uid, ChatID: 8201})
	ch, err = s.GetChannelByChat(ctx, uid, 8201)
	require.NoError(t, err)
	assert.True(t, ch.IsBotAdmin, "bot_added должен вернуть флаг is_bot_admin")
}

// TestFSM_EarlyChannelCheck — вход в шаг «Каналы публикации» без каналов с
// админкой уводит в «Мои каналы», а не даёт пройти дальше.
func TestFSM_EarlyChannelCheck(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6103
	testutil.EnsureUser(t, s, uid, "creator")

	b.Handle(ctx, callbackUpdate(uid, util.CBCreateGiveaway))
	g, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)

	// Быстро прыгаем на шаг pub_channels (name → post_text → skip*3 → next).
	b.Handle(ctx, msgUpdate(uid, "Имя"))
	b.Handle(ctx, msgUpdate(uid, "Текст"))
	b.Handle(ctx, callbackUpdate(uid, util.CBFSMSkip)) // media
	b.Handle(ctx, callbackUpdate(uid, util.CBFSMSkip)) // button_text
	b.Handle(ctx, callbackUpdate(uid, util.CBFSMOption+"next")) // sub_channels (пусто — ок)

	// Шаг pub_channels: каналов нет → должен быть экран «Мои каналы»
	// (сообщение с инструкцией), а draft_step остаётся pub_channels.
	gg, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepPubChannels, gg.DraftStep)

	warned := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Нет каналов, где бот — админ") {
			warned = true
		}
	}
	assert.True(t, warned, "юзер должен получить предупреждение о каналах ДО шага публикации")
}

// ---------- FSM условная навигация ----------

// TestFSM_BackSkipsConditionalSteps — «Назад» не попадает на пропущенные шаги:
// при finish_mode=count из finish_count назад идём к finish_mode (не finish_at),
// при publish_now назад от finish_mode идёт к pub_channels (не publish_at).
func TestFSM_BackSkipsConditionalSteps(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6201
	testutil.EnsureUser(t, s, uid, "creator")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8301, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	b.Handle(ctx, callbackUpdate(uid, util.CBCreateGiveaway))
	g, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	_ = g

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	send := func(text string) { b.Handle(ctx, msgUpdate(uid, text)) }
	curStep := func() string {
		gg, e := s.GetDraftGiveaway(ctx, uid)
		require.NoError(t, e)
		return gg.DraftStep
	}

	send("Имя")
	send("Текст")
	click(util.CBFSMSkip) // media
	click(util.CBFSMSkip) // button
	click(util.CBFSMOption + "next") // sub
	click(util.CBChannelTogglePub + strconv.FormatInt(ch.ID, 10))
	click(util.CBFSMOption + "next") // pub → publish_mode
	click(util.CBFSMOption + "publish_now") // скип publish_at → finish_mode
	require.Equal(t, fsm.StepFinishMode, curStep())

	// Назад от finish_mode — должен попасть на publish_mode (выбор «сразу/по
	// времени»), НЕ на пропущенный publish_at.
	click(util.CBFSMBack)
	assert.Equal(t, fsm.StepPublishMode, curStep(),
		"Назад после publish_now не должен попадать на пропущенный publish_at")

	// Вперёд снова: publish_at всё ещё «только что» → снова finish_mode.
	click(util.CBFSMOption + "publish_later")
	// publish_later ведёт на ввод даты — вводим будущую.
	send(util.FormatMoscowTime(time.Now().Add(48 * time.Hour)))
	require.Equal(t, fsm.StepFinishMode, curStep())
	click(util.CBFSMOption + "finish_count") // → finish_count
	require.Equal(t, fsm.StepFinishCount, curStep())

	// Назад от finish_count → finish_mode (не finish_at).
	click(util.CBFSMBack)
	assert.Equal(t, fsm.StepFinishMode, curStep(),
		"Назад после finish_count должен идти к finish_mode, не к finish_at")
}

// ---------- Редактура ----------

// TestEdit_PubChannelsAndFinish — редактируются каналы публикации и условие
// итогов у scheduled-розыгрыша.
func TestEdit_PubChannelsAndFinish(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6301
	testutil.EnsureUser(t, s, uid, "creator")

	ch1, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8401, Title: "Один", IsBotAdmin: true})
	require.NoError(t, err)
	ch2, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8402, Title: "Два", IsBotAdmin: true})
	require.NoError(t, err)

	past := time.Now().UTC().Add(-time.Hour)
	future := time.Now().UTC().Add(24 * time.Hour)
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO giveaways (owner_user_id, name, post_text, button_text,
		  publish_channel_ids, status, publish_at, finish_at, finish_mode, finish_participants_count, winners_count)
		VALUES ($1,'Ред','Текст','Участвовать', ARRAY[$2]::bigint[], 'scheduled', $3, $4, 'time', 0, 1)
	`, uid, ch1.ID, past, future)
	require.NoError(t, err)
	gws, err := s.ListGiveaways(ctx, uid, store.StatusScheduled)
	require.NoError(t, err)
	require.NotEmpty(t, gws)
	gid := gws[0].ID

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }

	// Открыть меню редактуры.
	click(util.CBGwEdit + strconv.FormatInt(gid, 10))

	// Тоггл канала публикации: добавить ch2.
	click(util.CBGwEditChTglPub + strconv.FormatInt(ch2.ID, 10) + ":" + strconv.FormatInt(gid, 10))
	g, err := s.GetGiveaway(ctx, gid, uid)
	require.NoError(t, err)
	assert.Len(t, g.PublishChannelIDs, 2, "канал публикации должен добавиться тогглом")

	// Снять ch1 — остаётся один канал.
	click(util.CBGwEditChTglPub + strconv.FormatInt(ch1.ID, 10) + ":" + strconv.FormatInt(gid, 10))
	g, err = s.GetGiveaway(ctx, gid, uid)
	require.NoError(t, err)
	assert.Equal(t, []int64{ch2.ID}, g.PublishChannelIDs)

	// Условие итогов → по количеству, вводим 50.
	click(util.CBGwEditField + "finish:" + strconv.FormatInt(gid, 10))
	click(util.CBGwEditSet + "finish:count:" + strconv.FormatInt(gid, 10))
	b.Handle(ctx, msgUpdate(uid, "50"))
	g, err = s.GetGiveaway(ctx, gid, uid)
	require.NoError(t, err)
	assert.Equal(t, "count", g.FinishMode)
	assert.Equal(t, 50, g.FinishParticipantsCount)
}

// ---------- Участие ----------

// TestParticipate_FromApp_NoBotMessages — участие через мини-апп
// (ParticipateFromApp): регистрация без ЕДИНОГО сообщения от бота
// (подтверждение рисует страница), notifiable=true если диалог начат.
func TestParticipate_FromApp_NoBotMessages(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	g, ch := setupActiveGiveawayWithChannel(t, s, 6401)
	mock.IsSubscriberMap = map[int64]map[int64]bool{ch.ChatID: {6401: true}}

	before := len(mock.SnapshotSends())
	res := b.ParticipateFromApp(g.ID, 6401)

	assert.True(t, res.Ok, "участие через мини-апп должно регистрировать, err=%s", res.Err)
	assert.True(t, res.Notifiable, "юзер с начатым диалогом — notifiable")
	n, err := s.CountParticipants(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)

	after := len(mock.SnapshotSends())
	assert.Equal(t, before, after, "мини-апп-путь не должен слать сообщений от бота")

	// Идемпотентность: повторный заход — тоже Ok, без дублей.
	res = b.ParticipateFromApp(g.ID, 6401)
	assert.True(t, res.Ok)
	n, err = s.CountParticipants(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

// TestParticipate_FromApp_NotSubscribed — отказ с причиной, без регистрации.
func TestParticipate_FromApp_NotSubscribed(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	g, _ := setupActiveGiveawayWithChannel(t, s, 6402)
	res := b.ParticipateFromApp(g.ID, 6402)

	assert.False(t, res.Ok)
	assert.Contains(t, res.Err, "подпишитесь")
	n, err := s.CountParticipants(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, 0, n, "без подписки участник не регистрируется")
}

// TestParticipants_KickAndCSV — удаление участника (анти-накрутка) и CSV.
func TestParticipants_KickAndCSV(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	g, _ := setupActiveGiveawayWithChannel(t, s, 6501)
	testutil.EnsureUser(t, s, 777, "botuser")
	testutil.EnsureUser(t, s, 778, "human")
	_, err := s.AddParticipant(ctx, store.Participant{GiveawayID: g.ID, UserID: 777, Tickets: 1})
	require.NoError(t, err)
	_, err = s.AddParticipant(ctx, store.Participant{GiveawayID: g.ID, UserID: 778, Tickets: 1})
	require.NoError(t, err)

	click := func(p string) { b.Handle(ctx, callbackUpdate(6501, p)) }

	// Экран участников.
	click(util.CBGwParticipants + strconv.FormatInt(g.ID, 10))
	// Удаляем участника-накрутку.
	click(util.CBGwKickPart + strconv.FormatInt(g.ID, 10) + ":777")

	n, err := s.CountParticipants(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "накрутка удалена, честный участник остался")

	// CSV — файлом.
	click(util.CBGwCSVParts + strconv.FormatInt(g.ID, 10))
	csvSent := false
	for _, r := range mock.SnapshotSends() {
		if r.Method == "SendFile" && strings.Contains(r.Text, "participants_") {
			csvSent = true
		}
	}
	assert.True(t, csvSent, "CSV участников должен отправляться файлом")
}

// ---------- UTM без канала ----------

// TestUTM_QuickNoChannel — трекинг-ссылка из «Аналитика» создаётся без канала.
func TestUTM_QuickNoChannel(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6601
	testutil.EnsureUser(t, s, uid, "owner")

	b.Handle(ctx, callbackUpdate(uid, util.CBAnalytics))
	b.Handle(ctx, callbackUpdate(uid, util.CBUTMQuickCreate))
	b.Handle(ctx, msgUpdate(uid, "Реклама у Петрова"))

	links, err := s.ListUTMStats(ctx, uid, 0)
	require.NoError(t, err)
	_ = links // channel_id=0 фильтр может не вернуть; проверяем через все.
	all, err := s.ListUTMStatsAll(ctx, uid)
	require.NoError(t, err)
	found := false
	for _, l := range all {
		if l.Source == "Реклама у Петрова" {
			found = true
		}
	}
	assert.True(t, found, "быстрая UTM-ссылка должна создаться без канала")
}

// ---------- Итоги: экранирование ----------

// TestResults_EscapeNames — имена со скобками не ломают markdown-упоминания.
func TestResults_EscapeNames(t *testing.T) {
	g := &store.Giveaway{ID: 1, Name: "T", ResultsPostText: ""}
	ws := []scheduler.Winner{
		{Name: "Вася[Пупкин](x)", UserID: 1},
		{Name: "Обычный", UserID: 2},
	}
	txt := scheduler.BuildResultsText(g, ws)
	assert.Contains(t, txt, "max://user/1")
	assert.Contains(t, txt, "max://user/2", "второй победитель должен быть в списке")
	assert.Contains(t, txt, "\\[Пупкин\\]", "скобки в имени должны экранироваться")
}

// TestResults_AppendedEscape — то же для итогов в исходном посте.
func TestResults_AppendedEscape(t *testing.T) {
	g := &store.Giveaway{ID: 1, PostText: "Пост"}
	ws := []scheduler.Winner{{Name: "A(b)c", UserID: 9}}
	txt := scheduler.BuildResultsAppended(g, ws)
	assert.Contains(t, txt, "max://user/9")
	assert.Contains(t, txt, "\\(b\\)")
}

// ---------- Лампочка: хинт сообщением + Продолжить ----------

// TestParticipateButton_IsDeepLink — кнопка участия под постом: deep-link
// (открывает диалог бота; участие и подтверждение в чате, бот получает право
// писать юзеру — мини-апп-путь отключён по результатам живого тестирования).
func TestParticipateButton_IsDeepLink(t *testing.T) {
	kb := util.ParticipateKeyboardBuilder("Участвовать", 5, "mybot", 3, 42)
	btn := kb.Build().Payload.Buttons[0][0]
	assert.Equal(t, model.ButtonLink, btn.Type, "кнопка участия — ссылка (deep-link в чат)")
	assert.Contains(t, btn.URL, "max.ru/mybot/start/part_5")
	assert.Contains(t, btn.Text, "(3)", "счётчик участников в подписи кнопки")
}

// TestOpenAppResultsButton_WebAppNotEmpty — кнопка итогов с botUserID, но
// БЕЗ username (ранняя стадия старта) — заглушка вместо пустоты.
func TestOpenAppResultsButton_WebAppNotEmpty(t *testing.T) {
	sch := scheduler.New(nil, nil, nil)
	sch.SetBotUserID(99)
	// botUsername пуст — раньше web_app уходил пустым → "cannot be null".
	atts := sch.ResultsKeyboardForTest(&store.Giveaway{ID: 7})
	require.NotEmpty(t, atts)
	btn := atts[0].Payload.Buttons[0][0]
	assert.Equal(t, model.ButtonOpenApp, btn.Type)
	assert.NotEmpty(t, btn.WebApp, "web_app у кнопки итогов не должен быть пустым")
}

// TestMedia_NoDuplicateTokens — повторная отправка того же вложения (после
// «Назад») не дублирует медиа: «всего 1», а не 2.
func TestMedia_NoDuplicateTokens(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6901
	testutil.EnsureUser(t, s, uid, "creator")

	b.Handle(ctx, callbackUpdate(uid, util.CBCreateGiveaway))
	b.Handle(ctx, msgUpdate(uid, "Имя"))
	b.Handle(ctx, msgUpdate(uid, "Текст"))
	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, fsm.StepMedia, gg.DraftStep)

	// Сообщение с фото (токен t1).
	mediaMsg := func(token string) model.Update {
		msgSeq++
		return model.Update{
			UpdateType: model.UpdateMessageCreated,
			UserID:     uid,
			MessageID:  "mid.m." + token + "." + strconv.FormatInt(msgSeq, 10),
			User:       &model.User{UserID: uid, FirstName: "t"},
			Message: &model.MessageUpdate{Body: model.MessageBody{Attachments: []model.Attachment{
				{Type: model.AttachImage, Payload: model.Payload{Token: token}},
			}}},
		}
	}

	b.Handle(ctx, mediaMsg("t1"))
	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	require.Len(t, gg.MediaTokens, 1)

	// «Назад» с медиа-шага (на post_text) и вперёд снова на медиа — то же
	// фото не должно задублироваться.
	b.Handle(ctx, callbackUpdate(uid, util.CBFSMBack)) // media → post_text
	b.Handle(ctx, msgUpdate(uid, "Текст"))             // post_text → media
	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, fsm.StepMedia, gg.DraftStep)

	b.Handle(ctx, mediaMsg("t1"))
	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Len(t, gg.MediaTokens, 1, "тот же токен не должен дублироваться")

	// Сообщение честно говорит «уже добавлено».
	dupNotice := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "уже добавлено") {
			dupNotice = true
		}
	}
	assert.True(t, dupNotice)

	// Другое фото — добавляется: 2 всего.
	b.Handle(ctx, mediaMsg("t2"))
	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Len(t, gg.MediaTokens, 2)
}

// TestFSM_HintMessageFlow — лампочка шлёт хинт сообщением (алертов в MAX нет),
// «Продолжить» возвращает к текущему шагу, draft_step не менялся.
func TestFSM_HintMessageFlow(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6701
	testutil.EnsureUser(t, s, uid, "creator")

	b.Handle(ctx, callbackUpdate(uid, util.CBCreateGiveaway))
	b.Handle(ctx, msgUpdate(uid, "Имя"))
	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, fsm.StepPostText, gg.DraftStep)

	// Лампочка — сообщение с подсказкой и кнопками Продолжить/Отменить.
	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	click(util.CBFSMHint)

	hintSent := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.HasPrefix(r.Text, "💡") {
			hintSent = true
		}
	}
	assert.True(t, hintSent, "подсказка должна приходить сообщением (💡 ...)")

	// «Продолжить» — возврат к шагу post_text.
	click(util.CBFSMContinue)
	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepPostText, gg.DraftStep, "после Продолжить — тот же шаг")

	// Ввод обрабатывается дальше (шаг живой).
	b.Handle(ctx, msgUpdate(uid, "Текст поста"))
	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepMedia, gg.DraftStep)
}

// TestFSM_FirstStepNoBack — на первом шаге нет «Назад» (клавиатура 💡+Отмена),
// и fsm_back с него никуда не уводит.
func TestFSM_FirstStepNoBack(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6702
	testutil.EnsureUser(t, s, uid, "creator")

	b.Handle(ctx, callbackUpdate(uid, util.CBCreateGiveaway))
	g, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, fsm.StepName, g.DraftStep)

	// Клавиатура первого шага не содержит Назад.
	assert.NotContains(t, util.FirstStepKeyboard().Build().Payload.Buttons[0][1].Text, "Назад")

	// Даже если прислать fsm_back — остаёмся на name.
	b.Handle(ctx, callbackUpdate(uid, util.CBFSMBack))
	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepName, gg.DraftStep)
}

// ---------- Багфиксы итерации (ручное тестирование) ----------

// fwdUpdate — пересланное сообщение из канала.
func fwdUpdate(userID int64, text string) model.Update {
	return model.Update{
		UpdateType: model.UpdateMessageCreated,
		UserID:     userID,
		MessageID:  "mid.fwd." + strconv.FormatInt(time.Now().UnixNano(), 10),
		User:       &model.User{UserID: userID, FirstName: "tester"},
		Message: &model.MessageUpdate{
			Link: &model.LinkedMessage{Type: model.LinkTypeForward, ChatID: 999},
			Body: model.MessageBody{Text: text},
		},
	}
}

// TestInputValidation_ForwardOutsideFlow — пересылка поста вне любого флоу:
// контекстная подсказка (не эхо «используйте кнопки» без объяснений).
func TestInputValidation_ForwardOutsideFlow(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6801
	testutil.EnsureUser(t, s, uid, "tester")

	b.Handle(ctx, fwdUpdate(uid, "пост из канала"))

	last := ""
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid {
			last = r.Text
		}
	}
	assert.Contains(t, last, "переслали пост", "должна быть человеческая подсказка про пересланный пост")
	assert.Contains(t, last, "Добавить канал", "и путь решения: как добавить канал")
}

// TestInputValidation_ForwardOnTextStep — форвард на текстовом шаге отклоняется
// с пояснением, шаг не двигается.
func TestInputValidation_ForwardOnTextStep(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6802
	testutil.EnsureUser(t, s, uid, "creator")

	b.Handle(ctx, callbackUpdate(uid, util.CBCreateGiveaway))
	b.Handle(ctx, msgUpdate(uid, "Имя"))
	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, fsm.StepPostText, gg.DraftStep)

	b.Handle(ctx, fwdUpdate(uid, "текст чужого поста"))

	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepPostText, gg.DraftStep, "форвард не должен advancing шаг")
	assert.NotEqual(t, "текст чужого поста", gg.PostText, "текст форварда не должен сохраниться в пост")

	rejected := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "пересланное сообщение") {
			rejected = true
		}
	}
	assert.True(t, rejected, "должно быть пояснение про пересланный сообщение")
}

// TestInputValidation_TextOnButtonStep — текст на кнопочном шаге (режим
// публикации) отклоняется, шаг перерисовывается.
func TestInputValidation_TextOnButtonStep(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6803
	testutil.EnsureUser(t, s, uid, "creator")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8801, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	click(util.CBCreateGiveaway)
	b.Handle(ctx, msgUpdate(uid, "Имя"))
	b.Handle(ctx, msgUpdate(uid, "Текст"))
	click(util.CBFSMSkip)
	click(util.CBFSMSkip)
	click(util.CBFSMOption + "next")
	click(util.CBChannelTogglePub + strconv.FormatInt(ch.ID, 10))
	click(util.CBFSMOption + "next")
	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, fsm.StepPublishMode, gg.DraftStep)

	// Шлём мусорный текст на кнопочном шаге.
	b.Handle(ctx, msgUpdate(uid, "хуйня какая-то"))

	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepPublishMode, gg.DraftStep, "шаг не должен меняться от текста")

	rejected := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "не нужен текст") {
			rejected = true
		}
	}
	assert.True(t, rejected, "должно быть пояснение «здесь кнопки»")
}

// TestDraftDelete_ReturnsToDraftsList — после удаления черновика показывается
// список черновиков с кнопками, а не голое сообщение.
func TestDraftDelete_ReturnsToDraftsList(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6810
	testutil.EnsureUser(t, s, uid, "creator")

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	click(util.CBCreateGiveaway)
	b.Handle(ctx, msgUpdate(uid, "Мой черновик"))
	b.Handle(ctx, msgUpdate(uid, "Текст"))
	// Отменяем — черновик именованный, сохраняется.
	click(util.CBFSMCancel)

	gws, err := s.ListGiveaways(ctx, uid, store.StatusDraft)
	require.NoError(t, err)
	require.Len(t, gws, 1)

	// Удаляем через карточку.
	click(util.CBGwDelete + strconv.FormatInt(gws[0].ID, 10))

	sends := mock.SnapshotSends()
	deletedMsg, listShown := false, false
	for _, r := range sends {
		if r.UserID == uid && strings.Contains(r.Text, "Черновик удалён") {
			deletedMsg = true
		}
		if r.UserID == uid && (strings.Contains(r.Text, "Выберите розыгрыш") || strings.Contains(r.Text, "Здесь пока пусто") || strings.Contains(r.Text, "пусто")) {
			listShown = true
		}
	}
	assert.True(t, deletedMsg, "должно быть сообщение об удалении")
	assert.True(t, listShown, "после удаления должен вернуться список черновиков (с кнопками)")
}

// TestFSM_HintContinue_ChannelStep — «Продолжить» после подсказки на шаге
// каналов РЕНДЕРИТ пикер заново (новым сообщением), а не редактирует старое.
func TestFSM_HintContinue_ChannelStep(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6811
	testutil.EnsureUser(t, s, uid, "creator")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8811, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	click(util.CBCreateGiveaway)
	b.Handle(ctx, msgUpdate(uid, "Имя"))
	b.Handle(ctx, msgUpdate(uid, "Текст"))
	click(util.CBFSMSkip)
	click(util.CBFSMSkip)
	click(util.CBFSMOption + "next") // sub → pub

	pickersBefore := 0
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Каналы публикации") {
			pickersBefore++
		}
	}
	require.Equal(t, 1, pickersBefore, "исходный пикер отправлен один раз")

	// Лампочка → Продолжить.
	click(util.CBFSMHint)
	click(util.CBFSMContinue)

	pickersAfter := 0
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Каналы публикации") {
			pickersAfter++
		}
	}
	assert.Equal(t, 2, pickersAfter, "после «Продолжить» пикер должен прийти СНОВА (видимый возврат)")

	// Шаг не изменился, выбор канала по-прежнему работает.
	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepPubChannels, gg.DraftStep)
	click(util.CBChannelTogglePub + strconv.FormatInt(ch.ID, 10))
	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Contains(t, gg.PublishChannelIDs, ch.ID)
}

// TestDraft_CancelThenResume — отмена (именованный черновик сохраняется) →
// продолжение создания из «Черновики» работает, ввод двигает шаг.
func TestDraft_CancelThenResume(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6812
	testutil.EnsureUser(t, s, uid, "creator")

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	click(util.CBCreateGiveaway)
	b.Handle(ctx, msgUpdate(uid, "Возратимся"))
	b.Handle(ctx, msgUpdate(uid, "Текст поста"))
	click(util.CBFSMCancel) // именованный → сохранён в черновики

	gws, err := s.ListGiveaways(ctx, uid, store.StatusDraft)
	require.NoError(t, err)
	require.Len(t, gws, 1)
	gid := gws[0].ID
	require.Equal(t, fsm.StepMedia, gws[0].DraftStep, "шаг сохранён")

	// Продолжаем создание из карточки черновика.
	click(util.CBGwEdit + strconv.FormatInt(gid, 10))

	// Приглашение шага media отправлено.
	promptShown := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Медиа") {
			promptShown = true
		}
	}
	assert.True(t, promptShown, "после «Продолжить создание» должен показаться шаг сохранённого этапа")

	// Ввод дальше работает: пропускаем медиа → шаг кнопки.
	click(util.CBFSMSkip)
	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepButtonText, gg.DraftStep, "после resume ввод должен advancing шаги")
}

// TestDraft_ResumeStaleStep — старый черновик с удалённым шагом (referral)
// продолжается с первого шага, а не молчит.
func TestDraft_ResumeStaleStep(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6813
	testutil.EnsureUser(t, s, uid, "creator")

	_, err := s.Pool().Exec(ctx, `
		INSERT INTO giveaways (owner_user_id, name, post_text, status, draft_step)
		VALUES ($1, 'Старый', 'текст', 'draft', 'referral')
	`, uid)
	require.NoError(t, err)
	gws, err := s.ListGiveaways(ctx, uid, store.StatusDraft)
	require.NoError(t, err)
	require.Len(t, gws, 1)

	b.Handle(ctx, callbackUpdate(uid, util.CBGwEdit+strconv.FormatInt(gws[0].ID, 10)))

	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.FirstStep, gg.DraftStep, "устаревший шаг должен сбрасываться на первый")

	namePrompt := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Название конкурса") {
			namePrompt = true
		}
	}
	assert.True(t, namePrompt, "должен показаться промпт первого шага (не пустое сообщение)")
}

// TestEdit_FinishSwitchToTime_PromptsDate — смена режима итогов на «по времени»
// без даты запрашивает дату (раньше молча оставляла режим без finish_at).
func TestEdit_FinishSwitchToTime_PromptsDate(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6820
	testutil.EnsureUser(t, s, uid, "creator")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8820, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	past := time.Now().UTC().Add(-time.Hour)
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO giveaways (owner_user_id, name, post_text, publish_channel_ids, status,
		  publish_at, finish_mode, finish_participants_count, winners_count)
		VALUES ($1,'Финиш','Текст', ARRAY[$2]::bigint[], 'scheduled', $3, 'count', 50, 1)
	`, uid, ch.ID, past)
	require.NoError(t, err)
	gws, err := s.ListGiveaways(ctx, uid, store.StatusScheduled)
	require.NoError(t, err)
	require.NotEmpty(t, gws)
	gid := gws[0].ID

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	click(util.CBGwEdit + strconv.FormatInt(gid, 10))
	click(util.CBGwEditField + "finish:" + strconv.FormatInt(gid, 10))
	click(util.CBGwEditSet + "finish:time:" + strconv.FormatInt(gid, 10))

	// Бот должен запросить дату итогов.
	asked := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "дату итогов") {
			asked = true
		}
	}
	assert.True(t, asked, "переключение на «по времени» без даты должно запросить дату")

	// Вводим дату — сохраняется.
	b.Handle(ctx, msgUpdate(uid, util.FormatMoscowTime(time.Now().Add(48*time.Hour))))
	g, err := s.GetGiveaway(ctx, gid, uid)
	require.NoError(t, err)
	assert.Equal(t, "time", g.FinishMode)
	require.NotNil(t, g.FinishAt)
}

// TestEdit_SectionsComprehensive — прогон всех секций редактуры scheduled:
// текстовые поля, числа, капча, место итогов, медиа-очистка, время публикации.
func TestEdit_SectionsComprehensive(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6821
	testutil.EnsureUser(t, s, uid, "creator")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8821, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	past := time.Now().UTC().Add(-time.Hour)
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO giveaways (owner_user_id, name, post_text, button_text, publish_channel_ids, status,
		  publish_at, finish_at, finish_mode, winners_count, captcha_mode, media_tokens)
		VALUES ($1,'Компл','Текст','Участвовать', ARRAY[$2]::bigint[], 'scheduled',
		  $3, now() + interval '7 days', 'time', 1, 'none', '[]'::jsonb)
	`, uid, ch.ID, past)
	require.NoError(t, err)
	gws, err := s.ListGiveaways(ctx, uid, store.StatusScheduled)
	require.NoError(t, err)
	require.NotEmpty(t, gws)
	gid := gws[0].ID
	id := strconv.FormatInt(gid, 10)

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }

	// Название.
	click(util.CBGwEditField + "name:" + id)
	b.Handle(ctx, msgUpdate(uid, "Новое имя"))
	// Текст поста.
	click(util.CBGwEditField + "post_text:" + id)
	b.Handle(ctx, msgUpdate(uid, "Новый текст поста"))
	// Кнопка.
	click(util.CBGwEditField + "button_text:" + id)
	b.Handle(ctx, msgUpdate(uid, "Жми"))
	// Победители.
	click(util.CBGwEditField + "winners:" + id)
	b.Handle(ctx, msgUpdate(uid, "5"))
	// Сообщение победителям.
	click(util.CBGwEditField + "winners_msg:" + id)
	b.Handle(ctx, msgUpdate(uid, "Пишите в личку"))
	// Текст итогов.
	click(util.CBGwEditField + "results_text:" + id)
	b.Handle(ctx, msgUpdate(uid, "Итоги тут"))
	// Время публикации.
	click(util.CBGwEditField + "publish_at:" + id)
	b.Handle(ctx, msgUpdate(uid, util.FormatMoscowTime(time.Now().Add(24*time.Hour))))
	// Капча → да.
	click(util.CBGwEditField + "captcha:" + id)
	click(util.CBGwEditSet + "captcha:all:" + id)
	// Итоги → отдельным постом.
	click(util.CBGwEditField + "results_loc:" + id)
	click(util.CBGwEditSet + "results_original:0:" + id)
	// Медиа → очистить.
	click(util.CBGwEditField + "media:" + id)
	click(util.CBGwEditSet + "media:clear:" + id)

	g, err := s.GetGiveaway(ctx, gid, uid)
	require.NoError(t, err)
	assert.Equal(t, "Новое имя", g.Name)
	assert.Equal(t, "Новый текст поста", g.PostText)
	assert.Equal(t, "Жми", g.ButtonText)
	assert.Equal(t, 5, g.WinnersCount)
	assert.Equal(t, "Пишите в личку", g.WinnersMessage)
	assert.Equal(t, "Итоги тут", g.ResultsPostText)
	assert.NotNil(t, g.PublishAt)
	assert.True(t, g.PublishAt.After(time.Now()), "publish_at обновлён на будущее")
	assert.Equal(t, store.CaptchaAll, g.CaptchaMode)
	assert.False(t, g.ResultsInOriginalPost)
	assert.Empty(t, g.MediaTokens)

	// Меню редактуры active скрывает время публикации.
	_, err = s.Pool().Exec(ctx, `UPDATE giveaways SET status='active' WHERE id=$1`, gid)
	require.NoError(t, err)
	click(util.CBGwEdit + id)
	// Смотрим последнее меню редактуры (старые сообщения содержали publish_at).
	lastMenu := ""
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Редактирование") {
			lastMenu = r.Text
		}
	}
	require.NotEmpty(t, lastMenu, "меню редактуры должно открыться")
	assert.NotContains(t, lastMenu, "Время публикации", "у active не должно быть кнопки «Время публикации» в меню правки")
}
