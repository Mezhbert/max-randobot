package handlers_test

// Тесты утверждения поста из миниаппа: Approval-создание отправляет пост в чат
// с кнопками Да/Нет; «Да» публикует (scheduled), «Нет» оставляет черновик.
// Плюс автогенерация уникальных ссылок под розыгрыш (идемпотентность).

import (
	"context"
	"strconv"
	"strings"
	"testing"

	"github.com/maxbot/giveawaybot/internal/handlers"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/testutil"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAppGiveawayApproval_YesPublishes(t *testing.T) {
	b, s, mock, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 7001
	testutil.EnsureUser(t, s, uid, "approver")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 9101, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	gid, vmsg, err := b.CreateGiveawayFromApp(ctx, uid, handlers.GiveawayDraftFromApp{
		Name: "Тест утверждения", PostText: "Пост на утверждение", ButtonText: "Участвовать",
		PubChannels: []int64{ch.ID}, SubChannels: []int64{ch.ID},
		PublishNow: true, FinishMode: "time", FinishAt: "01.01.2099 21:00",
		Winners: 1, Approval: true,
	})
	require.NoError(t, err)
	assert.Empty(t, vmsg)
	assert.Greater(t, gid, int64(0))

	// Черновик остался draft; в чат ушли пост + вопрос с Да/Нет.
	g, err := s.GetGiveawayAny(ctx, gid)
	require.NoError(t, err)
	assert.Equal(t, store.StatusDraft, g.Status, "до «Да» розыгрыш не публикуется")

	approveAsked := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == uid && strings.Contains(r.Text, "Утверждаем публикацию?") {
			approveAsked = true
			var payloads []string
			for _, btn := range r.Buttons {
				payloads = append(payloads, btn.Payload)
			}
			assert.Contains(t, strings.Join(payloads, " "), util.CBApproveGwYes)
			assert.Contains(t, strings.Join(payloads, " "), util.CBApproveGwNo)
		}
	}
	assert.True(t, approveAsked, "вопрос с Да/Нет должен прийти в чат")

	// «Да» → запланирован/опубликован (publishNow в тестах не сеттится → scheduled).
	b.Handle(ctx, callbackUpdate(uid, util.CBApproveGwYes+int64String(gid)))
	g, err = s.GetGiveawayAny(ctx, gid)
	require.NoError(t, err)
	assert.Equal(t, store.StatusScheduled, g.Status, "«Да» переводит в scheduled")

	// Повторное «Да» — уже обработано, статус не меняется (idempotent UX).
	b.Handle(ctx, callbackUpdate(uid, util.CBApproveGwYes+int64String(gid)))
	assert.Equal(t, store.StatusScheduled, g.Status)
}

func TestAppGiveawayApproval_NoKeepsDraft(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 7002
	testutil.EnsureUser(t, s, uid, "editor")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 9102, Title: "Ch2", IsBotAdmin: true})
	require.NoError(t, err)

	gid, _, err := b.CreateGiveawayFromApp(ctx, uid, handlers.GiveawayDraftFromApp{
		Name: "Нет — на правку", PostText: "Пост", ButtonText: "Участвовать",
		PubChannels: []int64{ch.ID}, PublishNow: true,
		FinishMode: "time", FinishAt: "01.01.2099 21:00", Winners: 1, Approval: true,
	})
	require.NoError(t, err)

	b.Handle(ctx, callbackUpdate(uid, util.CBApproveGwNo+int64String(gid)))
	g, err := s.GetGiveawayAny(ctx, gid)
	require.NoError(t, err)
	assert.Equal(t, store.StatusDraft, g.Status, "«Нет» оставляет черновик для правки")
}

func TestAppGiveawayApproval_ValidationSurfaces(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 7003
	testutil.EnsureUser(t, s, uid, "validator")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 9103, Title: "Ch3", IsBotAdmin: true})
	require.NoError(t, err)

	// Прошедшая дата итогов: создание ок (approval не валидирует даты),
	// но «Да» отклоняет с авторским текстом — розыгрыш остаётся draft.
	gid, _, err := b.CreateGiveawayFromApp(ctx, uid, handlers.GiveawayDraftFromApp{
		Name: "Протухшая дата", PostText: "Пост", ButtonText: "Участвовать",
		PubChannels: []int64{ch.ID}, PublishNow: true,
		FinishMode: "time", FinishAt: "01.01.2020 21:00", Winners: 1, Approval: true,
	})
	require.NoError(t, err)
	b.Handle(ctx, callbackUpdate(uid, util.CBApproveGwYes+int64String(gid)))
	g, err := s.GetGiveawayAny(ctx, gid)
	require.NoError(t, err)
	assert.Equal(t, store.StatusDraft, g.Status, "невалидная дата — не публикуется")
}

func TestTrackingForGiveaway_Idempotent(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 7004
	testutil.EnsureUser(t, s, uid, "linker")
	ch1, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 9104, Title: "A", IsBotAdmin: true})
	require.NoError(t, err)
	ch2, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 9105, Title: "B", IsBotAdmin: true})
	require.NoError(t, err)

	g, err := s.CreateDraftGiveaway(ctx, uid, "")
	require.NoError(t, err)
	// Имя приходит из визарда (шаг 1) — до генерации (фикс «без названия»).
	links, vmsg, err := b.CreateTrackingForGiveaway(ctx, uid, g.ID, []int64{ch1.ID, ch2.ID}, "Конкурс Новый год")
	require.NoError(t, err)
	assert.Empty(t, vmsg)
	require.Len(t, links, 2, "скоп = по ссылке на каждый канал")
	for _, l := range links {
		assert.Contains(t, l.LinkURL, "?startapp=u_")
		assert.Equal(t, "unique", l.LinkType)
		assert.Contains(t, l.Name, "Конкурс Новый год", "имя содержит название розыгрыша")
	}
	assert.Contains(t, links[0].Name, " | ", "паттерн имени: розыгрыш | канал")

	// Повторная генерация — дублей нет, скоп возвращается полностью.
	links2, _, err := b.CreateTrackingForGiveaway(ctx, uid, g.ID, []int64{ch1.ID, ch2.ID}, "")
	require.NoError(t, err)
	assert.Len(t, links2, 2, "скоп без дублей")

	all, err := s.ListUTMLinksByGiveaway(ctx, g.ID)
	require.NoError(t, err)
	assert.Len(t, all, 2, "в БД ровно две ссылки под розыгрыш")
	for _, l := range all {
		assert.Equal(t, g.ID, l.GiveawayID, "ссылки привязаны к розыгрышу")
	}

	// Ручная ссылка из Трекинга привязывается к скопу розыгрыша.
	_, _, err = b.CreateTrackingLinkFromApp(ctx, uid, handlers.TrackingLinkFromApp{
		Mode: "ads", ChannelID: ch1.ID, Name: "Ручная супер", LinkType: "super",
		PostRef: "https://max.ru/x/1", GiveawayID: g.ID,
	})
	require.NoError(t, err)
	scope, _, err := b.GiveawayLinksForApp(ctx, uid, g.ID)
	require.NoError(t, err)
	assert.Len(t, scope, 3, "ручная ссылка входит в скоп")
	found := false
	for _, l := range scope {
		if l.Name == "Ручная супер" {
			found = true
		}
	}
	assert.True(t, found, "ручная ссылка видна в скопе розыгрыша")
}

func TestAppGiveaway_SubChannelsSet(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 7007
	testutil.EnsureUser(t, s, uid, "subs")
	ch1, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 9107, Title: "A", IsBotAdmin: true})
	require.NoError(t, err)
	ch2, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 9108, Title: "B", IsBotAdmin: true})
	require.NoError(t, err)

	// Явный ВЫБОР: подписка только на ch2 (ch1 публикуется, но подписка не нужна).
	gid, _, err := b.CreateGiveawayFromApp(ctx, uid, handlers.GiveawayDraftFromApp{
		Name: "Подписки вручную", PostText: "Пост", ButtonText: "Участвовать",
		PubChannels: []int64{ch1.ID, ch2.ID}, SubChannels: []int64{ch2.ID},
		SubChannelsSet: true, PublishNow: true,
		FinishMode: "time", FinishAt: "01.01.2099 21:00", Winners: 1, Approval: true,
	})
	require.NoError(t, err)
	g, err := s.GetGiveawayAny(ctx, gid)
	require.NoError(t, err)
	require.Len(t, g.SubscribeChannelIDs, 1)
	assert.Equal(t, ch2.ID, g.SubscribeChannelIDs[0], "подписки ровно как выбрал юзер")

	// НЕ выбирал (дефолт) — подписки = публикации.
	gid2, _, err := b.CreateGiveawayFromApp(ctx, uid, handlers.GiveawayDraftFromApp{
		Name: "Дефолт подписок", PostText: "Пост", ButtonText: "Участвовать",
		PubChannels: []int64{ch1.ID, ch2.ID},
		PublishNow: true, FinishMode: "time", FinishAt: "01.01.2099 21:00", Winners: 1, Approval: true,
	})
	require.NoError(t, err)
	g2, err := s.GetGiveawayAny(ctx, gid2)
	require.NoError(t, err)
	assert.Len(t, g2.SubscribeChannelIDs, 2, "без явного выбора подписки = публикации")
}

func int64String(v int64) string { return strconv.FormatInt(v, 10) }

func TestAppGiveaway_DraftGoneFallback(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 7005
	testutil.EnsureUser(t, s, uid, "gcvictim")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 9106, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	// Черновик «исчез» (удалён между шагами визарда) — create не должен падать
	// «not found» (живой инцидент 16.11), а прозрачно создать новый.
	gid, vmsg, err := b.CreateGiveawayFromApp(ctx, uid, handlers.GiveawayDraftFromApp{
		GiveawayID: 999999, Name: "После GC", PostText: "Пост", ButtonText: "Участвовать",
		PubChannels: []int64{ch.ID}, PublishNow: true,
		FinishMode: "time", FinishAt: "01.01.2099 21:00", Winners: 1, Approval: true,
	})
	require.NoError(t, err)
	assert.Empty(t, vmsg)
	assert.NotEqual(t, int64(999999), gid, "должен создаться НОВЫЙ черновик")
	g, err := s.GetGiveawayAny(ctx, gid)
	require.NoError(t, err)
	assert.Equal(t, store.StatusDraft, g.Status)
}

func TestDeleteEmptyDrafts_KeepsFresh(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 7006
	testutil.EnsureUser(t, s, uid, "gckeep")

	fresh, err := s.CreateDraftGiveaway(ctx, uid, "")
	require.NoError(t, err) // активный визард: пустой, но МОЛОДОЙ

	require.NoError(t, s.DeleteEmptyDrafts(ctx, uid))
	_, err = s.GetGiveawayAny(ctx, fresh.ID)
	assert.NoError(t, err, "свежий пустой черновик (в визарде) GC не трогает")
}

func TestRenameGiveaway_FixesLinks(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 7008
	testutil.EnsureUser(t, s, uid, "renamer")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 9109, Title: "Канал А", IsBotAdmin: true})
	require.NoError(t, err)

	// Старый «безымянный» черновик со ссылкой в старом формате имени.
	g, err := s.CreateDraftGiveaway(ctx, uid, "")
	require.NoError(t, err)
	code := "RenameT1"
	_, err = s.CreateUTMLink(ctx, uid, ch.ID, "Розыгрыш: без названия", "referral",
		"Розыгрыш: без названия", "", "", "", code, 0, g.ID, "unique")
	require.NoError(t, err)

	vmsg, err := b.RenameGiveawayFromApp(ctx, uid, g.ID, "Правильное имя")
	require.NoError(t, err)
	assert.Empty(t, vmsg)

	gg, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, "Правильное имя", gg.Name, "черновик переименован")

	links, err := s.ListUTMLinksByGiveaway(ctx, g.ID)
	require.NoError(t, err)
	require.Len(t, links, 1)
	assert.Equal(t, "Правильное имя | Канал А", links[0].Source,
		"ссылка переименована в «Новое имя | Канал»")
}

func TestDeleteGiveaway_FromApp(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 7009
	testutil.EnsureUser(t, s, uid, "deleter")

	gid, _, err := b.CreateGiveawayFromApp(ctx, uid, handlers.GiveawayDraftFromApp{
		Name: "На удаление", PostText: "Пост", ButtonText: "Участвовать",
		PublishNow: true, FinishMode: "time", FinishAt: "01.01.2099 21:00",
		Winners: 1, Approval: true,
	})
	require.NoError(t, err)
	require.NoError(t, s.DeleteGiveaway(ctx, gid, uid))
	_, err = s.GetGiveawayAny(ctx, gid)
	assert.Error(t, err, "черновик удалён")
	err = s.DeleteGiveaway(ctx, gid, uid)
	assert.Error(t, err, "повторное удаление — не найден")
}

func TestArchiveGiveaway(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 7010
	testutil.EnsureUser(t, s, uid, "archiver")

	g, err := s.CreateDraftGiveaway(ctx, uid, "")
	require.NoError(t, err)

	require.NoError(t, s.SetGiveawayArchived(ctx, g.ID, uid, true))
	gg, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.True(t, gg.Archived, "скрыт в архив")

	require.NoError(t, s.SetGiveawayArchived(ctx, g.ID, uid, false))
	gg, err = s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.False(t, gg.Archived, "возвращён из архива")

	// Завершённый тоже удаляется (guard снят).
	require.NoError(t, s.MarkFinished(ctx, g.ID))
	require.NoError(t, s.DeleteGiveaway(ctx, g.ID, uid))
	_, err = s.GetGiveawayAny(ctx, g.ID)
	assert.Error(t, err, "завершённый удалён навсегда")
}
