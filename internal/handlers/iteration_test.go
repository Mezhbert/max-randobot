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

// TestParticipate_QuietConfirmation — после регистрации ОДНА тихая карточка
// (SendTextQuiet), не обычный SendText.
func TestParticipate_QuietConfirmation(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()

	g, ch := setupActiveGiveawayWithChannel(t, s, 6401)
	mock.IsSubscriberMap = map[int64]map[int64]bool{ch.ChatID: {6401: true}}

	b.Handle(ctx, msgUpdate(6401, "/start part_"+strconv.FormatInt(g.ID, 10)))
	// onBotStarted шлёт стартовый экран; участие — через deep-link payload.
	body := []byte(`{"update_type":"bot_started","user":{"user_id":6401}}`)
	b.OnDeepLinkStart(ctx, body, "part_"+strconv.FormatInt(g.ID, 10))

	n, err := s.CountParticipants(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, 1, n, "участник должен зарегистрироваться")

	quiets := 0
	for _, r := range mock.SnapshotSends() {
		if r.Method == "SendTextQuiet" && r.UserID == 6401 {
			quiets++
		}
	}
	assert.Equal(t, 1, quiets, "подтверждение — ровно одна тихая карточка")
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

// TestFSM_HintMessageFlow — лампочка шлёт хинт сообщением (алертов в MAX нет),
// «Продолжить» возвращает на текущий шаг, draft_step не менялся.
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
