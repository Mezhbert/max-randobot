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
	s.UpdateGiveawayFieldAny(ctx, g.ID, "name", "Конкурс Новый год")

	links, vmsg, err := b.CreateTrackingForGiveaway(ctx, uid, g.ID, []int64{ch1.ID, ch2.ID})
	require.NoError(t, err)
	assert.Empty(t, vmsg)
	require.Len(t, links, 2, "по ссылке на каждый канал")
	for _, l := range links {
		assert.Contains(t, l.LinkURL, "?startapp=u_")
		assert.Equal(t, "unique", l.LinkType)
	}

	// Повторный вызов (перезашли в визард) — дублей нет.
	links2, _, err := b.CreateTrackingForGiveaway(ctx, uid, g.ID, []int64{ch1.ID, ch2.ID})
	require.NoError(t, err)
	assert.Empty(t, links2, "существующие ссылки не пересоздаются")

	all, err := s.ListUTMLinksByGiveaway(ctx, g.ID)
	require.NoError(t, err)
	assert.Len(t, all, 2, "в БД ровно две ссылки под розыгрыш")
	for _, l := range all {
		assert.Equal(t, g.ID, l.GiveawayID, "ссылки привязаны к розыгрышу")
	}
}

func int64String(v int64) string { return strconv.FormatInt(v, 10) }
