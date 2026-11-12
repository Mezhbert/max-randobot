package handlers_test

// Тесты большой итерации: reply-keyboard, каналы (refresh/ссылка/ранний чек),
// условная навигация FSM, редактура (медиа/каналы), тихое участие,
// участники (удаление), UTM без канала, экранирование итогов.

import (
	"context"
	"errors"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/maxbot/giveawaybot/internal/fsm"
	"github.com/maxbot/giveawaybot/internal/handlers"
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

// TestFSM_EarlyChannelCheck — шаг 2 без каналов: пикер предлагает
// «➕ Подключить канал», шаг остаётся channel (валидация до, не в конце).
func TestFSM_EarlyChannelCheck(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6103
	testutil.EnsureUser(t, s, uid, "creator")

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	click(util.CBCreateGiveaway)
	b.Handle(ctx, msgUpdate(uid, "Текст поста"))

	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepChannel, gg.DraftStep, "после поста — шаг канала")

	// Шаг канала без каналов: кнопка подключения, «Далее» недоступен.
	connectOffered := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Шаг 2 из 10") {
			connectOffered = true
		}
	}
	assert.True(t, connectOffered, "шаг канала должен показаться (с ➕ Подключить канал)")

	// «Далее» без канала — уведомление, шаг не меняется.
	click(util.CBFSMNext)
	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepChannel, gg.DraftStep, "без канала «Далее» не проходит")
}

// ---------- FSM условная навигация ----------

// TestFSM_BackNavigation — «← Назад» идёт на предыдущий шаг с сохранёнными
// значениями; с первого шага — выход в меню (черновик жив).
func TestFSM_BackNavigation(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6201
	testutil.EnsureUser(t, s, uid, "creator")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8301, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	curStep := func() string {
		gg, e := s.GetDraftGiveaway(ctx, uid)
		require.NoError(t, e)
		return gg.DraftStep
	}

	click(util.CBCreateGiveaway)
	b.Handle(ctx, msgUpdate(uid, "Текст"))
	click(util.CBFSMOption + "ch:" + strconv.FormatInt(ch.ID, 10))
	click(util.CBFSMNext) // -> name
	require.Equal(t, fsm.StepName, curStep())
	click(util.CBFSMOption + "name:0")
	click(util.CBFSMNext) // -> subs
	require.Equal(t, fsm.StepSubs, curStep())

	// Назад: subs -> name (значение сохранено).
	click(util.CBFSMBack)
	assert.Equal(t, fsm.StepName, curStep(), "Назад с подписок — к названию")
	gg, _ := s.GetDraftGiveaway(ctx, uid)
	assert.NotEmpty(t, gg.Name, "название сохранено при возврате")

	// Назад с первого шага (post) — меню, черновик жив.
	click(util.CBFSMBack) // name -> channel
	click(util.CBFSMBack) // channel -> post
	click(util.CBFSMBack) // post -> меню
	gg, err = s.GetGiveawayAny(ctx, gg.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusDraft, gg.Status, "черновик сохранён после выхода с первого шага")
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

// TestUTM_QuickLink_ChannelPickerFlow — Трекинг → выбор канала → название →
// вопрос о конструкторе → «Простая ссылка» → готово.
func TestUTM_QuickLink_ChannelPickerFlow(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6601
	testutil.EnsureUser(t, s, uid, "owner")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8601, Title: "Мой канал", IsBotAdmin: true})
	require.NoError(t, err)

	// Шаг 1: выбор канала — пикер со списком каналов.
	b.Handle(ctx, callbackUpdate(uid, util.CBUTMQuickCreate))
	pickerShown := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Выберите канал") {
			pickerShown = true
		}
	}
	require.True(t, pickerShown, "первым шагом должен быть выбор канала")

	// Шаг 2: канал выбран → запрос названия.
	b.Handle(ctx, callbackUpdate(uid, util.CBUTMQuickChSel+strconv.FormatInt(ch.ID, 10)))
	// Шаг 3: название → вопрос «Настроить метки?».
	b.Handle(ctx, msgUpdate(uid, "Реклама у Петрова"))
	builderAsked := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "конструктором") {
			builderAsked = true
		}
	}
	require.True(t, builderAsked, "после названия — вопрос о конструкторе меток")

	// Шаг 4: «Простая ссылка» — готово.
	b.Handle(ctx, callbackUpdate(uid, util.CBUTMBuildNo))

	all, err := s.ListUTMStatsAll(ctx, uid)
	require.NoError(t, err)
	require.Len(t, all, 1, "простая ссылка создаётся после «Простая ссылка»")
	assert.Equal(t, "Реклама у Петрова", all[0].Source)
	assert.Equal(t, "", all[0].PostRef, "простая ссылка — без поста-источника")
	assert.InDelta(t, 0.0, all[0].Cost, 0.01)

	stats, err := s.ListUTMStats(ctx, uid, ch.ID)
	require.NoError(t, err)
	require.Len(t, stats, 1)

	linkShown := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "?startapp=u_") {
			linkShown = true
		}
	}
	assert.True(t, linkShown, "бот должен выдать startapp-ссылку мини-аппа")
}

// TestUTM_QuickLink_NoChannels — пикер без каналов: подсказка вместо флоу.
func TestUTM_QuickLink_NoChannels(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6603
	testutil.EnsureUser(t, s, uid, "owner")

	b.Handle(ctx, callbackUpdate(uid, util.CBUTMQuickCreate))
	hinted := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Сначала добавьте канал") {
			hinted = true
		}
	}
	assert.True(t, hinted, "без каналов — подсказка добавить канал в «Мои каналы»")
}

// TestUTM_SkipPost — кнопка «⏭ Без поста-источника» в БИЛДЕРЕ (P7): ссылка
// создаётся, post_ref пустой.
func TestUTM_SkipPost(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6602
	testutil.EnsureUser(t, s, uid, "owner")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8602, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	b.Handle(ctx, callbackUpdate(uid, util.CBUTMCreate+strconv.FormatInt(ch.ID, 10)))
	b.Handle(ctx, msgUpdate(uid, "Сторис"))
	b.Handle(ctx, callbackUpdate(uid, util.CBUTMSkipPost))
	b.Handle(ctx, callbackUpdate(uid, util.CBUTMSkipCost))

	all, err := s.ListUTMStatsAll(ctx, uid)
	require.NoError(t, err)
	require.Len(t, all, 1)
	assert.Equal(t, "Сторис", all[0].Source)
	assert.Equal(t, "", all[0].PostRef, "пропуск поста → пустой post_ref")
	assert.InDelta(t, 0.0, all[0].Cost, 0.01)
}

// ---------- UTM: клик должен приводить в канал, а не упираться в бота ----------

// botStartedUpdate — update bot_started (как при клике по диплинку).
func botStartedUpdate(userID int64) model.Update {
	return model.Update{
		UpdateType: model.UpdateBotStarted,
		UserID:     userID,
		User:       &model.User{UserID: userID, FirstName: "visitor"},
	}
}

// TestUTM_ChannelClick_ShowsChannelButton — клик по диплинку приводит
// посетителя на экран канала с кнопкой подписки.
func TestUTM_ChannelClick_ShowsChannelButton(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const owner, visitor = 6701, 6702
	testutil.EnsureUser(t, s, owner, "owner")
	testutil.EnsureUser(t, s, visitor, "visitor")

	ch, err := s.AddChannel(ctx, store.Channel{
		OwnerUserID: owner, ChatID: -670200, Title: "Мой канал",
		Link: "mychannel", IsPublic: true, IsBotAdmin: true,
	})
	require.NoError(t, err)

	// Простая ссылка (без конструктора).
	b.Handle(ctx, callbackUpdate(owner, util.CBUTMCreate+strconv.FormatInt(ch.ID, 10)))
	b.Handle(ctx, msgUpdate(owner, "Реклама"))
	b.Handle(ctx, callbackUpdate(owner, util.CBUTMBuildNo))

	stats, err := s.ListUTMStats(ctx, owner, ch.ID)
	require.NoError(t, err)
	require.Len(t, stats, 1)

	// Клик по диплинку → экран канала с кнопкой-ссылкой.
	b.SetPendingStart(visitor, "utm_"+stats[0].Code)
	b.Handle(ctx, botStartedUpdate(visitor))

	sawChannel := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID != visitor {
			continue
		}
		for _, btn := range r.Buttons {
			if btn.URL == "https://max.ru/mychannel" {
				sawChannel = true
			}
		}
		if strings.Contains(r.Text, "Мой канал") {
			sawChannel = sawChannel || strings.Contains(r.Text, "канал")
		}
	}
	assert.True(t, sawChannel, "посетитель должен получить кнопку на канал")
}

// TestUTM_PostRefURL_LeadsToPost — конструктор с постом-источником: ссылка
// max.ru/... сохраняется, клик по легаси-диплинку ведёт на конкретный пост.
func TestUTM_PostRefURL_LeadsToPost(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const owner, visitor = 6711, 6712
	testutil.EnsureUser(t, s, owner, "owner")
	testutil.EnsureUser(t, s, visitor, "visitor")

	ch, err := s.AddChannel(ctx, store.Channel{
		OwnerUserID: owner, ChatID: -671200, Title: "Постовый",
		Link: "postovy", IsPublic: true, IsBotAdmin: true,
	})
	require.NoError(t, err)

	// Конструктор: название → да → 5 скипов меток → пост → розыгрышей нет
	// (авто-скип) → стоимость.
	b.Handle(ctx, callbackUpdate(owner, util.CBUTMCreate+strconv.FormatInt(ch.ID, 10)))
	b.Handle(ctx, msgUpdate(owner, "Реклама"))
	b.Handle(ctx, callbackUpdate(owner, util.CBUTMBuildYes))
	for i := 0; i < 5; i++ { // src, med, cmp, term, cont
		b.Handle(ctx, callbackUpdate(owner, util.CBUTMStageSkip))
	}
	b.Handle(ctx, msgUpdate(owner, "https://max.ru/postovy/42")) // пост
	b.Handle(ctx, msgUpdate(owner, "500"))                      // стоимость

	stats, err := s.ListUTMStats(ctx, owner, ch.ID)
	require.NoError(t, err)
	require.Len(t, stats, 1)
	assert.Equal(t, "https://max.ru/postovy/42", stats[0].PostRef)
	assert.InDelta(t, 500.0, stats[0].Cost, 0.01)

	// Клик по легаси-диплинку utm_<code> → экран с кнопкой на ПОСТ.
	code := stats[0].Code
	b.SetPendingStart(visitor, "utm_"+code)
	b.Handle(ctx, botStartedUpdate(visitor))

	sawPost, sawChannel := false, false
	for _, r := range mock.SnapshotSends() {
		if r.UserID != visitor {
			continue
		}
		for _, btn := range r.Buttons {
			if btn.URL == "https://max.ru/postovy/42" {
				sawPost = true
			}
			if btn.URL == "https://max.ru/postovy" {
				sawChannel = true
			}
		}
	}
	assert.True(t, sawPost, "у посетителя должна быть кнопка на конкретный пост")
	assert.True(t, sawChannel, "и кнопка на канал")
}

// TestUTM_PrivateChannel_NoLinkButton — приватный канал (без публичной ссылки):
// экран с названием есть, link-кнопки нет.
func TestUTM_PrivateChannel_NoLinkButton(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const owner, visitor = 6711, 6712
	testutil.EnsureUser(t, s, owner, "owner")
	testutil.EnsureUser(t, s, visitor, "visitor")

	ch, err := s.AddChannel(ctx, store.Channel{
		OwnerUserID: owner, ChatID: -671200, Title: "Секретный канал", IsBotAdmin: true,
	})
	require.NoError(t, err)

	b.Handle(ctx, callbackUpdate(owner, util.CBUTMCreate+strconv.FormatInt(ch.ID, 10)))
	b.Handle(ctx, msgUpdate(owner, "Реклама"))
	b.Handle(ctx, callbackUpdate(owner, util.CBUTMSkipPost))
	b.Handle(ctx, callbackUpdate(owner, util.CBUTMSkipCost))

	stats, err := s.ListUTMStats(ctx, owner, ch.ID)
	require.NoError(t, err)
	require.Len(t, stats, 1)

	b.SetPendingStart(visitor, "utm_"+stats[0].Code)
	b.Handle(ctx, botStartedUpdate(visitor))

	sawName, hasAnyLink := false, false
	for _, r := range mock.SnapshotSends() {
		if r.UserID != visitor {
			continue
		}
		if strings.Contains(r.Text, "Секретный канал") {
			sawName = true
		}
		for _, btn := range r.Buttons {
			if btn.Type == model.ButtonLink {
				hasAnyLink = true
			}
		}
	}
	assert.True(t, sawName, "название канала должно показываться")
	assert.False(t, hasAnyLink, "у приватного канала не может быть link-кнопки")
}

// TestUTM_QuickClick_ShowsStartMenu — ЛЕГАСИ-ссылка без канала (остались с
// прошлых версий): клик по диплинку utm_<code> даёт обычный стартовый экран.
func TestUTM_QuickClick_ShowsStartMenu(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const owner, visitor = 6721, 6722
	testutil.EnsureUser(t, s, owner, "owner")
	testutil.EnsureUser(t, s, visitor, "visitor")

	// Сеем канал-менее ссылку напрямую (новые ссылки всегда канальные, P6).
	_, err := s.CreateUTMLink(ctx, owner, 0, "Реклама", "referral", "Реклама", "", "", "", "deadbeef01", 0, 0)
	require.NoError(t, err)

	b.SetPendingStart(visitor, "utm_deadbeef01")
	b.Handle(ctx, botStartedUpdate(visitor))

	sawStart := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == visitor && strings.Contains(r.Text, "розыгрыши") {
			sawStart = true
		}
	}
	assert.True(t, sawStart, "легаси-ссылка без канала → стартовый экран бота")
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

// TestParticipateButton_LinkAndFallback — кнопка участия под постом (P3b):
// с именем бота — startapp-ССЫЛКА в мини-апп участия (механика проверена
// живьём на трекинге), без имени — callback-fallback (старые посты, участие
// в чате). Счётчик участников — в подписи в обоих режимах.
func TestParticipateButton_LinkAndFallback(t *testing.T) {
	// Основной путь: startapp-ссылка на страницу участия.
	kb := util.ParticipateKeyboardBuilder("Участвовать", 5, 3, "my_bot")
	btn := kb.Build().Payload.Buttons[0][0]
	assert.Equal(t, model.ButtonLink, btn.Type, "кнопка участия — ссылка в мини-апп")
	assert.Equal(t, "https://max.ru/my_bot?startapp=part_5", btn.URL)
	assert.Contains(t, btn.Text, "(3)", "счётчик участников в подписи кнопки")

	// Fallback: имя бота ещё не узнали — callback (участие в чате бота).
	kbCb := util.ParticipateKeyboardBuilder("Участвовать", 5, 3, "")
	btnCb := kbCb.Build().Payload.Buttons[0][0]
	assert.Equal(t, model.ButtonCallback, btnCb.Type, "без имени бота — callback-fallback")
	assert.Equal(t, "part:5", btnCb.Payload)
	assert.Contains(t, btnCb.Text, "(3)")
}

// TestResultsButton_Link — кнопка итогов (P4): startapp-ссылка на страницу
// результатов; без имени бота кнопки нет (nil).
func TestResultsButton_Link(t *testing.T) {
	kb := util.ResultsKeyboard(7, "my_bot")
	require.NotNil(t, kb)
	btn := kb.Build().Payload.Buttons[0][0]
	assert.Equal(t, model.ButtonLink, btn.Type)
	assert.Equal(t, "https://max.ru/my_bot?startapp=results_7", btn.URL)
	assert.Contains(t, btn.Text, "Проверить результаты")

	assert.Nil(t, util.ResultsKeyboard(7, ""), "без имени бота кнопки итогов не должно быть")
}


// TestMedia_NoDuplicateTokens — повторная отправка того же вложения на шаге 1
// не дублирует токен (пост уже заполнен → медиа докидывается на месте).
func TestMedia_NoDuplicateTokens(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6901
	testutil.EnsureUser(t, s, uid, "creator")

	mediaMsg := func(token, caption string) model.Update {
		msgSeq++
		return model.Update{
			UpdateType: model.UpdateMessageCreated,
			UserID:     uid,
			MessageID:  "mid.test." + strconv.FormatInt(msgSeq, 10),
			User:       &model.User{UserID: uid, FirstName: "tester"},
			Message: &model.MessageUpdate{
				Body: model.MessageBody{
					Text: caption,
					Attachments: []model.Attachment{
						{Type: model.AttachImage, Payload: model.Payload{Token: token}},
					},
				},
			},
		}
	}

	b.Handle(ctx, callbackUpdate(uid, util.CBCreateGiveaway))
	g, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)

	// Медиа без текста — валидный первый ввод (шаг заполнен, остаёмся на посте).
	b.Handle(ctx, mediaMsg("t1", ""))
	gg, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	require.Len(t, gg.MediaTokens, 1)
	assert.Equal(t, fsm.StepPost, gg.DraftStep, "медиа-пост остаётся на шаге 1 (можно докинуть)")

	// То же вложение ещё раз — дубля нет.
	b.Handle(ctx, mediaMsg("t1", ""))
	gg, err = s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Len(t, gg.MediaTokens, 1, "повторное вложение не дублируется")

	// Другое вложение — добавляется.
	b.Handle(ctx, mediaMsg("t2", ""))
	gg, err = s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Len(t, gg.MediaTokens, 2)
}

// TestFSM_CancelThenContinue — «✕ Отменить» → «▶ Продолжить»: шаг
// возвращается на место (морфинг), создание продолжается.
func TestFSM_CancelThenContinue(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6701
	testutil.EnsureUser(t, s, uid, "creator")

	b.Handle(ctx, callbackUpdate(uid, util.CBCreateGiveaway))
	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }

	// Пауза и продолжение на первом шаге.
	click(util.CBFSMCancel)
	click(util.CBFSMContinue)

	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepPost, gg.DraftStep, "после Продолжить — тот же шаг")

	// Ввод обрабатывается дальше (шаг живой): пост → шаг 2.
	b.Handle(ctx, msgUpdate(uid, "Текст поста"))
	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepChannel, gg.DraftStep)

	// Экран паузы действительно показывался.
	paused := false
	for _, r := range mock.SnapshotSends() {
		if strings.Contains(r.Text, "Остановить создание") {
			paused = true
		}
	}
	assert.True(t, paused)
}

// TestFSM_FirstStepNoBack — «← Назад» с первого шага: выход в меню с
// сохранением черновика (мануал §3: с шага 1 — меню).
func TestFSM_FirstStepNoBack(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6702
	testutil.EnsureUser(t, s, uid, "creator")

	b.Handle(ctx, callbackUpdate(uid, util.CBCreateGiveaway))
	g, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, fsm.StepPost, g.DraftStep)

	// Клавиатура первого шага не содержит Назад.
	kb := b.StepKeyboardForTest(ctx, uid, g, fsm.StepPost)
	noBack := true
	for _, row := range kb.Build().Payload.Buttons {
		for _, btn := range row {
			if strings.Contains(btn.Text, "Назад") {
				noBack = false
			}
		}
	}
	assert.True(t, noBack, "на первом шаге нет «Назад»")

	// fsm_back с первого шага — в меню (черновик жив).
	b.Handle(ctx, callbackUpdate(uid, util.CBFSMBack))
	gg, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusDraft, gg.Status)
	menuShown := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "приостановлено") {
			menuShown = true
		}
	}
	assert.True(t, menuShown, "Назад с первого шага — в меню (черновик жив)")
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

// TestInputValidation_ForwardOnTextStep — форвард на шаге 1 (пост) отклоняется
// с пояснением, шаг не двигается.
func TestInputValidation_ForwardOnTextStep(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6802
	testutil.EnsureUser(t, s, uid, "creator")

	b.Handle(ctx, callbackUpdate(uid, util.CBCreateGiveaway))
	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, fsm.StepPost, gg.DraftStep)

	// Форвард чужого поста — НЕ текст для своего поста... но на шаге 1 форвард
	// обрабатывается как пост? Нет: пост — свой контент, форвард отклоняется.
	b.Handle(ctx, fwdUpdate(uid, "текст чужого поста"))

	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepPost, gg.DraftStep, "форвард не должен двигать шаг")
	assert.Empty(t, gg.PostText, "текст форварда не должен сохраниться в пост")
}

// TestInputValidation_TextOnButtonStep — текст на кнопочном шаге (подписки)
// отклоняется, шаг не меняется.
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
	b.Handle(ctx, msgUpdate(uid, "Текст поста"))
	click(util.CBFSMOption + "ch:" + strconv.FormatInt(ch.ID, 10))
	click(util.CBFSMNext)
	click(util.CBFSMOption + "name:0")
	click(util.CBFSMNext)
	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	require.Equal(t, fsm.StepSubs, gg.DraftStep)

	// Шлём мусорный текст на кнопочном шаге.
	b.Handle(ctx, msgUpdate(uid, "хуйня какая-то"))

	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepSubs, gg.DraftStep, "шаг не должен меняться от текста")

	rejected := false
	for _, r := range mock.SnapshotSends() {
		if strings.Contains(r.Text, "не нужен текст") {
			rejected = true
		}
	}
	for _, e := range mock.Edits {
		if strings.Contains(e.Text, "не нужен текст") {
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
	b.Handle(ctx, msgUpdate(uid, "Текст поста розыгрыша"))
	click(util.CBFSMCancel)  // экран подтверждения
	click(util.CBFSMExitYes) // без названия — удаляется... даём имя через вариант:
	// (чтобы черновик сохранился, у него должно быть имя — создадим заново)
	click(util.CBCreateGiveaway)
	b.Handle(ctx, msgUpdate(uid, "Текст поста розыгрыша"))
	click(util.CBFSMNext) // у нас нет каналов — «Далее» не пройдёт; ставим шаг напрямую
	g, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	require.NoError(t, s.UpdateGiveawayField(ctx, g.ID, "name", "Мой черновик"))
	click(util.CBFSMCancel)
	click(util.CBFSMExitYes)

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

// TestFSM_ChannelStep_SelectAndLock — выбор канала морфит шаг («✓»),
// повторный выбор другого канала заменяет первый (single-select).
func TestFSM_ChannelStep_SelectAndLock(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6811
	testutil.EnsureUser(t, s, uid, "creator")
	ch1, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8811, Title: "Один", IsBotAdmin: true})
	require.NoError(t, err)
	ch2, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8812, Title: "Два", IsBotAdmin: true})
	require.NoError(t, err)

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	click(util.CBCreateGiveaway)
	b.Handle(ctx, msgUpdate(uid, "Текст"))
	click(util.CBFSMOption + "ch:" + strconv.FormatInt(ch1.ID, 10))
	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, []int64{ch1.ID}, gg.PublishChannelIDs)
	assert.Equal(t, fsm.StepChannel, gg.DraftStep, "выбор канала не переводит шаг")

	// Другой канал — заменяет (не добавляется).
	click(util.CBFSMOption + "ch:" + strconv.FormatInt(ch2.ID, 10))
	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, []int64{ch2.ID}, gg.PublishChannelIDs, "выбор канала — single-select")
}

// TestDraft_CancelThenResume — «Да, выйти» (именованный черновик сохраняется)
// → продолжение создания из «Черновики» работает, ввод двигает шаг.
func TestDraft_CancelThenResume(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6812
	testutil.EnsureUser(t, s, uid, "creator")

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	click(util.CBCreateGiveaway)
	b.Handle(ctx, msgUpdate(uid, "Текст поста"))
	g, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	require.NoError(t, s.UpdateGiveawayField(ctx, g.ID, "name", "Вернёмся"))
	click(util.CBFSMCancel)
	click(util.CBFSMExitYes) // именованный → сохранён в черновики

	gws, err := s.ListGiveaways(ctx, uid, store.StatusDraft)
	require.NoError(t, err)
	require.Len(t, gws, 1)
	gid := gws[0].ID
	assert.Equal(t, fsm.StepChannel, gws[0].DraftStep, "шаг сохранён (после текста поста — канал)")

	// Продолжаем создание из карточки черновика.
	click(util.CBGwEdit + strconv.FormatInt(gid, 10))

	// Приглашение шага поста отправлено.
	promptShown := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Шаг 1 из 10") {
			promptShown = true
		}
	}
	assert.True(t, promptShown, "после «Продолжить создание» должен показаться сохранённый шаг")
}

// TestDraft_ResumeStaleStep — старый черновик с legacy-шагом (promotion)
// продолжается с нормализованного шага, а не молчит.
func TestDraft_ResumeStaleStep(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6813
	testutil.EnsureUser(t, s, uid, "creator")

	_, err := s.Pool().Exec(ctx, `
		INSERT INTO giveaways (owner_user_id, name, post_text, status, draft_step)
		VALUES ($1, 'Старый', 'текст', 'draft', 'promotion')
	`, uid)
	require.NoError(t, err)
	gws, err := s.ListGiveaways(ctx, uid, store.StatusDraft)
	require.NoError(t, err)
	require.Len(t, gws, 1)

	b.Handle(ctx, callbackUpdate(uid, util.CBGwEdit+strconv.FormatInt(gws[0].ID, 10)))

	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, fsm.StepPost, gg.DraftStep, "legacy-шаг нормализуется к первому")

	promptShown := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Шаг 1 из 10") {
			promptShown = true
		}
	}
	assert.True(t, promptShown)
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
	// Рефералка → включить (P2: раньше пункт отсутствовал в меню).
	click(util.CBGwEditField + "referral:" + id)
	click(util.CBGwEditSet + "referral:1:" + id)
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
	assert.True(t, g.ReferralEnabled, "рефералка должна включаться из редактуры (P2)")
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

// TestEdit_ApplySetStatusGuard — applyEditSet достижим crafted-callback'ом
// в обход showEditMenu: завершённый розыгрыш править нельзя (P2).
func TestEdit_ApplySetStatusGuard(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6945
	testutil.EnsureUser(t, s, uid, "creator")

	gid := seedActiveGiveaway(t, s, uid, "none", false)
	_, err := s.Pool().Exec(ctx, `UPDATE giveaways SET status='finished', referral_enabled=false WHERE id=$1`, gid)
	require.NoError(t, err)

	// Crafted-вызов applyEditSet напрямую (минуя меню с проверкой статуса).
	b.Handle(ctx, callbackUpdate(uid, util.CBGwEditSet+"referral:1:"+strconv.FormatInt(gid, 10)))

	g, err := s.GetGiveaway(ctx, gid, uid)
	require.NoError(t, err)
	assert.False(t, g.ReferralEnabled, "завершённый розыгрыш нельзя править через crafted-callback")
}

// TestFSM_StartByTime_Confirm — старт «по времени»: publish_at ставится при
// выборе, publish_now сбрасывается; подтверждение → scheduled.
func TestFSM_StartByTime_Confirm(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6950
	testutil.EnsureUser(t, s, uid, "creator")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 8950, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	send := func(text string) { b.Handle(ctx, msgUpdate(uid, text)) }

	click(util.CBCreateGiveaway)
	send("Текст")
	click(util.CBFSMOption + "ch:" + strconv.FormatInt(ch.ID, 10))
	click(util.CBFSMNext)
	click(util.CBFSMOption + "name:0")
	click(util.CBFSMNext)
	click(util.CBFSMNext) // subs
	click(util.CBFSMNext) // button
	click(util.CBFSMNext) // winners
	click(util.CBFSMNext) // finish (завтра 21:00)
	require.Equal(t, fsm.StepStart, func() string {
		gg, e := s.GetDraftGiveaway(ctx, uid)
		require.NoError(t, e)
		return gg.DraftStep
	}())

	// Дефолт «Сразу» включён; переключаемся на «Через час».
	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.True(t, gg.PublishNow, "дефолт старта — сразу")
	click(util.CBFSMOption + "start:hour")
	gg, err = s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.False(t, gg.PublishNow, "start:hour сбрасывает publish_now")
	assert.NotNil(t, gg.PublishAt, "start:hour ставит publish_at")

	click(util.CBFSMNext) // extras
	click(util.CBFSMNext) // check
	click(util.CBFSMConfirm)

	gg, err = s.GetGiveawayAny(ctx, gg.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusScheduled, gg.Status)
	assert.False(t, gg.PublishNow)
}

// ---------- P0: капча юзеру без стартованного диалога ----------

// TestParticipate_CaptchaWaitStart_AutoResume — бот не может писать юзеру
// (SendImage падает): вместо «чёрной дыры» — сессия wait_start, а при первом
// bot_started капча приходит САМА, без /start и без стартового экрана.
func TestParticipate_CaptchaWaitStart_AutoResume(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6901
	testutil.EnsureUser(t, s, uid, "visitor")

	gid := seedActiveGiveaway(t, s, uid, "all", false)

	// Бот не может писать (у мока SendImage делит ошибку с SendText).
	mock.SendTextErr = errors.New("forbidden: dialog not started")
	b.Handle(ctx, callbackUpdate(uid, util.CBParticipate+strconv.FormatInt(gid, 10)))

	var st string
	var payload map[string]any
	sess, err := s.GetSessionPayload(ctx, uid)
	require.NoError(t, err)
	st, payload = sess.State, sess.Payload
	assert.Equal(t, store.SessionCaptchaWaitStart, st, "интент капчи запомнен")
	gotGid, _ := payload["giveaway_id"].(float64)
	assert.Equal(t, float64(gid), gotGid)

	// Юзер открывает бота (Начать) — бот теперь может писать: капча сама.
	mock.SendTextErr = nil
	b.Handle(ctx, botStartedUpdate(uid))

	sent := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && r.Method == "SendImage" {
			sent = true
		}
	}
	assert.True(t, sent, "капча должна прийти при первом старте бота автоматически")
	sess, err = s.GetSessionPayload(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, store.SessionCaptchaVerify, sess.State, "сессия перешла в режим ввода цифр")
}

// ---------- Реферальная система ----------

// TestReferral_CreditAndLimit — полный цикл: участник получает личную ссылку,
// друг стартует по ней бота и участвует → инвайтеру +1 билет; лимит 1
// исчерпывается вторым другом.
func TestReferral_CreditAndLimit(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const inviter, friend1, friend2 = 6911, 6912, 6913
	testutil.EnsureUser(t, s, inviter, "inviter")
	testutil.EnsureUser(t, s, friend1, "f1")
	testutil.EnsureUser(t, s, friend2, "f2")

	gid := seedActiveGiveaway(t, s, inviter, "none", true) // referral on, limit 1
	_, _ = s.Pool().Exec(ctx, `UPDATE giveaways SET referral_limit=1 WHERE id=$1`, gid)

	// Инвайтер участвует → получает личную ref-ссылку.
	b.Handle(ctx, callbackUpdate(inviter, util.CBParticipate+strconv.FormatInt(gid, 10)))
	code := ""
	for _, r := range mock.SnapshotSends() {
		if r.UserID == inviter {
			if m := regexp.MustCompile(`ref_([0-9a-f]+)`).FindStringSubmatch(r.Text); m != nil {
				code = m[1]
			}
		}
	}
	require.NotEmpty(t, code, "участнику должна прийти личная ref-ссылка")

	// Друг1 стартует бота по ссылке и участвует → инвайтеру +1 билет.
	b.SetPendingStart(friend1, "ref_"+code)
	b.Handle(ctx, botStartedUpdate(friend1))
	b.Handle(ctx, callbackUpdate(friend1, util.CBParticipate+strconv.FormatInt(gid, 10)))
	pi, err := s.GetParticipant(ctx, gid, inviter)
	require.NoError(t, err)
	assert.Equal(t, 2, pi.Tickets, "+1 билет за первого друга")

	// Друг2 — лимит (1) исчерпан: билета больше нет.
	b.SetPendingStart(friend2, "ref_"+code)
	b.Handle(ctx, botStartedUpdate(friend2))
	b.Handle(ctx, callbackUpdate(friend2, util.CBParticipate+strconv.FormatInt(gid, 10)))
	pi, err = s.GetParticipant(ctx, gid, inviter)
	require.NoError(t, err)
	assert.Equal(t, 2, pi.Tickets, "лимит приглашений исчерпан — билет не растёт")
}

// ---------- Жеребьёвка без отписавшихся ----------

// TestDraw_ExcludesUnsubscribed — отписавшийся от канала розыгрыша участник
// не попадает в список для выбора победителей.
func TestDraw_ExcludesUnsubscribed(t *testing.T) {
	_, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const owner, u1, u2 = 6921, 6922, 6923
	testutil.EnsureUser(t, s, owner, "own")
	testutil.EnsureUser(t, s, u1, "u1")
	testutil.EnsureUser(t, s, u2, "u2")

	g, ch := setupActiveGiveawayWithChannel(t, s, owner)
	for _, u := range []int64{u1, u2} {
		p, err := s.AddParticipant(ctx, store.Participant{GiveawayID: g.ID, UserID: u, Tickets: 1, CaptchaPassed: true})
		require.NoError(t, err)
		require.NoError(t, s.AddSubscriptionLog(ctx, p.ID, g.ID, ch.ID, u, "post"))
	}
	// u1 отписался.
	_, err := s.Pool().Exec(ctx, `
		UPDATE subscriptions_log SET unsubscribed_at=now()
		WHERE giveaway_id=$1 AND user_id=$2`, g.ID, u1)
	require.NoError(t, err)

	list, err := s.ListParticipantsForDrawVerified(ctx, g.ID)
	require.NoError(t, err)
	require.Len(t, list, 1, "отписавшийся исключён из жеребьёвки")
	assert.Equal(t, int64(u2), list[0].UserID)
}

// ---------- Копия розыгрыша и дефолты ----------

// TestCopyGiveaway — «Создать копию» завершённого: черновик с тем же контентом,
// имя с суффиксом, шаг — первый.
func TestCopyGiveaway(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6931
	testutil.EnsureUser(t, s, uid, "owner")

	src := seedActiveGiveaway(t, s, uid, "none", false)
	_, err := s.Pool().Exec(ctx, `UPDATE giveaways SET name='Оригинал', post_text='Пост X', button_text='Жми', status='finished' WHERE id=$1`, src)
	require.NoError(t, err)

	b.Handle(ctx, callbackUpdate(uid, util.CBGwCopy+strconv.FormatInt(src, 10)))

	draft, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "Оригинал (копия)", draft.Name)
	assert.Equal(t, "Пост X", draft.PostText)
	assert.Equal(t, "Жми", draft.ButtonText)
	assert.Equal(t, fsm.StepPost, draft.DraftStep)
	assert.Equal(t, store.StatusDraft, draft.Status)
}

// TestStartDraft_DefaultsFromLast — новый черновик наследует настройки
// последнего созданного розыгрыша (кнопка/капча/рефералка/каналы).
func TestStartDraft_DefaultsFromLast(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6941
	testutil.EnsureUser(t, s, uid, "owner")

	gid := seedActiveGiveaway(t, s, uid, "all", true)
	_, err := s.Pool().Exec(ctx, `UPDATE giveaways SET button_text='Жми' WHERE id=$1`, gid)
	require.NoError(t, err)

	b.Handle(ctx, callbackUpdate(uid, util.CBCreateGiveaway))
	draft, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "Жми", draft.ButtonText)
	assert.Equal(t, store.CaptchaAll, draft.CaptchaMode)
	assert.True(t, draft.ReferralEnabled)
}

// seedActiveGiveaway — активный розыгрыш без каналов (для участия).
func seedActiveGiveaway(t *testing.T, s *store.Store, owner int64, captcha string, referral bool) int64 {
	t.Helper()
	var id int64
	err := s.Pool().QueryRow(context.Background(), `
		INSERT INTO giveaways (owner_user_id, name, post_text, button_text, status,
			finish_mode, finish_participants_count, winners_count, captcha_mode, referral_enabled)
		VALUES ($1, 'T', 'пост', 'Участвовать', 'active', 'count', 1000, 1, $2, $3)
		RETURNING id
	`, owner, captcha, referral).Scan(&id)
	require.NoError(t, err)
	return id
}

// TestParticipate_CaptchaGatesRegistration — P3: при включённой капче клик
// «Участвовать» отправляет картинку, но НЕ регистрирует участника. Раньше
// registerParticipant вызывался сразу после отправки капчи — юзер становился
// участником до ввода кода, а верный ответ ловил «Вы уже участвуете!».
func TestParticipate_CaptchaGatesRegistration(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6961
	testutil.EnsureUser(t, s, uid, "player")

	gid := seedActiveGiveaway(t, s, uid, store.CaptchaAll, false)

	// Клик «Участвовать» (callback part:<id>).
	b.Handle(ctx, callbackUpdate(uid, util.CBParticipate+strconv.FormatInt(gid, 10)))

	// Капча отправлена картинкой в чат.
	captchaSent := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Капча для участия") {
			captchaSent = true
		}
	}
	assert.True(t, captchaSent, "капча должна прийти картинкой в чат")

	// ГЛАВНОЕ: участник ещё НЕ зарегистрирован — регистрация только после
	// верного ответа (handleCaptchaVerifyInput).
	n, err := s.CountParticipant(ctx, gid, uid)
	require.NoError(t, err)
	assert.Zero(t, n, "юзер НЕ должен становиться участником до ввода капчи")

	// Неверный ответ — участник по-прежнему не зарегистрирован.
	b.Handle(ctx, msgUpdate(uid, "0000"))
	n, err = s.CountParticipant(ctx, gid, uid)
	require.NoError(t, err)
	assert.Zero(t, n, "неверный ответ капчи не должен регистрировать участника")
}

// TestChannelStep_LiveAdminCheck — выбор канала с протухшим флагом
// is_bot_admin: «fsm_opt:ch» проверяет админство ЖИВЫМ запросом, канал не
// выбирается, флаг в БД обновляется.
func TestChannelStep_LiveAdminCheck(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 6951
	testutil.EnsureUser(t, s, uid, "creator")

	ch, err := s.AddChannel(ctx, store.Channel{
		OwnerUserID: uid, ChatID: -6951000, Title: "Мёртвый", IsBotAdmin: true,
	})
	require.NoError(t, err)
	mock.IsBotAdminMap = map[int64]bool{-6951000: false}

	b.Handle(ctx, callbackUpdate(uid, util.CBCreateGiveaway))
	b.Handle(ctx, msgUpdate(uid, "Текст"))

	// Выбор мёртвого канала — блокируется, флаг обновляется.
	b.Handle(ctx, callbackUpdate(uid, util.CBFSMOption+"ch:"+strconv.FormatInt(ch.ID, 10)))

	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Empty(t, gg.PublishChannelIDs, "мёртвый канал не выбирается")

	fresh, err := s.GetChannel(ctx, ch.ID, uid)
	require.NoError(t, err)
	assert.False(t, fresh.IsBotAdmin, "протухший флаг обновляется на live-значение")

	blocked := false
	for _, n := range mock.CallbackNotifs {
		if strings.Contains(n, "не админ") {
			blocked = true
		}
	}
	assert.True(t, blocked, "юзер получает уведомление о мёртвом канале")
}

// ---------- Трекинг: опциональный билдер меток + retention ----------

// utmFlowT — хелперы флоу создания ссылки.
type utmFlowT struct {
	t    *testing.T
	b    *handlers.Bot
	s    *store.Store
	mock *testutil.MockBotAPI
	uid  int64
	ch   store.Channel
}

func newUTMFlow(t *testing.T, uid int64) utmFlowT {
	t.Helper()
	b, s, mock, cleanup := newBot(t)
	t.Cleanup(cleanup)
	ctx := context.Background()
	testutil.EnsureUser(t, s, uid, "owner")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 9500 + uid, Title: "Канал", IsBotAdmin: true})
	require.NoError(t, err)
	return utmFlowT{t: t, b: b, s: s, mock: mock, uid: uid, ch: ch}
}

func (f utmFlowT) click(p string)    { f.b.Handle(context.Background(), callbackUpdate(f.uid, p)) }
func (f utmFlowT) send(text string)  { f.b.Handle(context.Background(), msgUpdate(f.uid, text)) }

// enterName — Трекинг → выбор канала → название (до вопроса о билдере).
func (f utmFlowT) enterName(name string) {
	f.click(util.CBUTMQuickCreate)
	f.click(util.CBUTMQuickChSel + strconv.FormatInt(f.ch.ID, 10))
	f.send(name)
}

// TestUTM_BuilderSimpleAndSkipAll — «Простая ссылка» и «скипнуть все шаги»
// дают ОДИНАКОВУЮ ссылку (одни дефолты), как требует ТЗ.
func TestUTM_BuilderSimpleAndSkipAll(t *testing.T) {
	f1 := newUTMFlow(t, 6961)
	f1.enterName("Закуп в TG")
	f1.click(util.CBUTMBuildNo)
	all1, err := f1.s.ListUTMStatsAll(context.Background(), f1.uid)
	require.NoError(t, err)
	require.Len(t, all1, 1)

	f2 := newUTMFlow(t, 6962)
	f2.enterName("Закуп в TG")
	f2.click(util.CBUTMBuildYes) // src
	for i := 0; i < 6; i++ {    // src, med, cmp, term, cont, post
		f2.click(util.CBUTMStageSkip)
	}
	f2.click(util.CBUTMGWSkip)    // розыгрышей нет — кнопка легитимна
	f2.click(util.CBUTMStageSkip) // cost
	all2, err := f2.s.ListUTMStatsAll(context.Background(), f2.uid)
	require.NoError(t, err)
	require.Len(t, all2, 1)

	assert.Equal(t, all1[0].Medium, all2[0].Medium, "скип всего = дефолт medium")
	assert.Equal(t, all1[0].Campaign, all2[0].Campaign, "скип всего = дефолт campaign")
	assert.Equal(t, all1[0].Source, all2[0].Source)
	assert.Equal(t, "referral", all2[0].Medium)
	assert.Equal(t, "Закуп в TG", all2[0].Campaign)
	assert.Empty(t, all2[0].UTMTerm)
	assert.Empty(t, all2[0].UTMContent)
	assert.Empty(t, all2[0].PostRef)
	assert.Zero(t, all2[0].GiveawayID)

	// Выдача — ОДНА startapp-ссылка, без анонимной/прямой альтернатив.
	linkShown := 0
	for _, r := range f2.mock.SnapshotSends() {
		if strings.Contains(r.Text, "?startapp=u_") {
			linkShown++
		}
		if strings.Contains(r.Text, "/r/") || strings.Contains(r.Text, "Прямая") {
			t.Fatalf("в выдаче не должно быть альтернатив: %s", r.Text)
		}
	}
	assert.GreaterOrEqual(t, linkShown, 1, "ссылка выдана")
}

// TestUTM_BuilderFull — полная сборка: метки, пост, привязка розыгрыша
// (включая черновик — по ТЗ), стоимость.
func TestUTM_BuilderFull(t *testing.T) {
	f := newUTMFlow(t, 6963)
	ctx := context.Background()

	var gid int64
	require.NoError(t, f.s.Pool().QueryRow(ctx, `
		INSERT INTO giveaways (owner_user_id, name, post_text, status, draft_step)
		VALUES ($1, 'Черновик-розыгрыш', 'п', 'draft', 'post') RETURNING id
	`, f.uid).Scan(&gid))

	f.enterName("Закуп у Иванова")
	f.click(util.CBUTMBuildYes)
	f.send("telegram")                 // utm_source
	f.send("cpc")                      // utm_medium
	f.send("november_launch")          // utm_campaign
	f.send("розыгрыш+iphone")          // utm_term
	f.send("banlink")                  // utm_content
	f.send("https://max.ru/post/123")  // пост
	f.click(util.CBUTMGW + strconv.FormatInt(gid, 10)) // привязка черновика
	f.send("1500")                     // стоимость

	all, err := f.s.ListUTMStatsAll(ctx, f.uid)
	require.NoError(t, err)
	require.Len(t, all, 1)
	st := all[0]
	assert.Equal(t, "telegram", st.Source, "utm_source перезаписан билдером")
	assert.Equal(t, "cpc", st.Medium)
	assert.Equal(t, "november_launch", st.Campaign)
	assert.Equal(t, "розыгрыш+iphone", st.UTMTerm)
	assert.Equal(t, "banlink", st.UTMContent)
	assert.Equal(t, "https://max.ru/post/123", st.PostRef)
	assert.Equal(t, gid, st.GiveawayID, "ссылка привязана к розыгрышу-черновику")
	assert.InDelta(t, 1500.0, st.Cost, 0.01)

	sawMarks, sawPost, sawCost := false, false, false
	for _, r := range f.mock.SnapshotSends() {
		if strings.Contains(r.Text, "utm_term=розыгрыш+iphone") {
			sawMarks = true
		}
		if strings.Contains(r.Text, "max.ru/post/123") {
			sawPost = true
		}
		if strings.Contains(r.Text, "1500") && strings.Contains(r.Text, "₽") {
			sawCost = true
		}
	}
	assert.True(t, sawMarks, "метки показаны в выдаче")
	assert.True(t, sawPost, "пост показан в выдаче")
	assert.True(t, sawCost, "стоимость показана в выдаче")
}

// TestUTM_UserAddedCreatesSubscriber — подписка атрибутированного юзера
// попадает в utm_subscribers (retention), отписка — фиксируется.
func TestUTM_UserAddedCreatesSubscriber(t *testing.T) {
	f := newUTMFlow(t, 6964)
	ctx := context.Background()

	f.enterName("Проверка подписки")
	f.click(util.CBUTMBuildNo)

	all, err := f.s.ListUTMStatsAll(ctx, f.uid)
	require.NoError(t, err)
	require.Len(t, all, 1)
	linkID := all[0].ID

	require.NoError(t, f.s.UpsertUser(ctx, store.User{UserID: 7001}))
	require.NoError(t, f.s.UTMRecordClick(ctx, linkID, 7001))
	f.b.Handle(ctx, model.Update{
		UpdateType: model.UpdateUserAdded,
		UserID:     7001,
		ChatID:     f.ch.ChatID,
		User:       &model.User{UserID: 7001, FirstName: "Visitor"},
	})
	ret, err := f.s.GetUTMLinkRetention(ctx, linkID)
	require.NoError(t, err)
	assert.Equal(t, 1, ret.Subscribed, "user_added создал подписчика ссылки")
	assert.Equal(t, 1, ret.StillIn)

	f.b.Handle(ctx, model.Update{
		UpdateType: model.UpdateUserRemoved,
		UserID:     7001,
		ChatID:     f.ch.ChatID,
		User:       &model.User{UserID: 7001, FirstName: "Visitor"},
	})
	ret, err = f.s.GetUTMLinkRetention(ctx, linkID)
	require.NoError(t, err)
	assert.Equal(t, 0, ret.StillIn, "user_removed зафиксировал отписку")
}
