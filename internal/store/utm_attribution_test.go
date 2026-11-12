package store

// Тест атрибуции подписок/отписок по трекинг-ссылкам. Регрессия: раньше
// UTMAccountSignup/Unsub сравнивали utm_links.channel_id (внутренний BIGSERIAL)
// с chat_id MAX из вебхука user_added (например -74970071115840) — они не
// совпадают НИКОГДА, поэтому signups/unsubs не засчитывались: трекинг-ссылки
// считали клики, но аналитика «какая закупка принесла подписчиков» не работала.

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUTM_AccountSignupByChatID(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, s.UpsertUser(ctx, User{UserID: 101, FirstName: "Clicker"}))
	require.NoError(t, s.UpsertUser(ctx, User{UserID: 102, FirstName: "Organic"}))
	owner := int64(100)
	require.NoError(t, s.UpsertUser(ctx, User{UserID: owner, FirstName: "Owner"}))

	// Канал: внутренний id выдаст БД, chat_id MAX — отрицательный.
	ch, err := s.AddChannel(ctx, Channel{
		OwnerUserID: owner, ChatID: -74970071115840, Title: "Рекламный", IsBotAdmin: true,
	})
	require.NoError(t, err)

	// Трекинг-ссылка на канал + клик юзера 101 (атрибуция).
	linkID, err := s.CreateUTMLink(ctx, owner, ch.ID, "Реклама у Иванова", "referral", "Реклама у Иванова", "", "", "", "ab12cd34ef", 1500, 0)
	require.NoError(t, err)
	require.NoError(t, s.UpsertUser(ctx, User{UserID: 101}))
	require.NoError(t, s.UTMRecordClick(ctx, linkID, 101))

	// Подписка юзера 101 в канал: вебхук приносит CHAT_ID, не внутренний id.
	_, attributed, err := s.UTMAccountSignup(ctx, 101, -74970071115840)
	require.NoError(t, err)
	assert.True(t, attributed, "подписка по chat_id MAX должна атрибутироваться трекинг-ссылке")

	link, err := s.GetUTMLinkByCode(ctx, "ab12cd34ef")
	require.NoError(t, err)
	assert.EqualValues(t, 1, link.Signups, "signups должен стать 1")
	assert.EqualValues(t, 1, link.Clicks, "клик посчитан")
	assert.InDelta(t, 1500.0, link.Cost, 0.01)

	// Отписка того же юзера — unsubs +1.
	require.NoError(t, s.UTMAccountUnsub(ctx, 101, -74970071115840))
	link, err = s.GetUTMLinkByCode(ctx, "ab12cd34ef")
	require.NoError(t, err)
	assert.EqualValues(t, 1, link.Unsubs, "отписка по chat_id должна считаться")

	// Юзер без атрибуции (органик) — не атрибутируется, счётчики не растут.
	_, attributed, err = s.UTMAccountSignup(ctx, 102, -74970071115840)
	require.NoError(t, err)
	assert.False(t, attributed, "органик не атрибутируется")
	link, _ = s.GetUTMLinkByCode(ctx, "ab12cd34ef")
	assert.EqualValues(t, 1, link.Signups, "signups не растёт от органика")
}

// TestUTM_Retention — удержание подписчиков ссылки: подписка/отписка
// событиями, окна 1/3/7/14 дней, «после розыгрыша», суточный поллинг.
func TestUTM_Retention(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	owner, u1, u2, u3 := int64(200), int64(201), int64(202), int64(203)
	for _, u := range []int64{owner, u1, u2, u3} {
		require.NoError(t, s.UpsertUser(ctx, User{UserID: u}))
	}
	ch, err := s.AddChannel(ctx, Channel{OwnerUserID: owner, ChatID: -200, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	// Розыгрыш для привязки (закончился 5 дней назад).
	var gid int64
	require.NoError(t, s.Pool().QueryRow(ctx, `
		INSERT INTO giveaways (owner_user_id, name, post_text, status, updated_at)
		VALUES ($1, 'R', 'p', 'finished', now() - interval '5 days') RETURNING id
	`, owner).Scan(&gid))

	linkID, err := s.CreateUTMLink(ctx, owner, ch.ID, "Закуп", "referral", "giveaway_1",
		"", "", "", "retention1", 600, gid)
	require.NoError(t, err)

	// Трое подписались по ссылке (в разное время).
	for _, u := range []int64{u1, u2, u3} {
		require.NoError(t, s.UpsertUTMSubscriber(ctx, linkID, u, -200))
	}
	// u1 отписался через 2 дня (после окна 1д, до 3д), u2 — через 10 дней,
	// u3 — держится. Точность в тесте задаём напрямую SQL.
	_, err = s.Pool().Exec(ctx, `
		UPDATE utm_subscribers SET subscribed_at = now() - interval '12 days',
			unsubscribed_at = now() - interval '10 days' WHERE utm_link_id=$1 AND user_id=$2
	`, linkID, u1)
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, `
		UPDATE utm_subscribers SET subscribed_at = now() - interval '12 days',
			unsubscribed_at = now() - interval '2 days' WHERE utm_link_id=$1 AND user_id=$2
	`, linkID, u2)
	require.NoError(t, err)
	_, err = s.Pool().Exec(ctx, `
		UPDATE utm_subscribers SET subscribed_at = now() - interval '12 days'
		WHERE utm_link_id=$1 AND user_id=$2
	`, linkID, u3)
	require.NoError(t, err)

	r, err := s.GetUTMLinkRetention(ctx, linkID)
	require.NoError(t, err)
	// u1 держался 2 дня (мимо 3д), u2 — 10 дней (мимо 14д), u3 — держится.
	assert.Equal(t, 3, r.Subscribed, "трое подписавшихся")
	assert.Equal(t, 1, r.StillIn, "u3 держится")
	assert.Equal(t, 3, r.Retained1d, "1д держались все (u1 отписался через 2д)")
	assert.Equal(t, 2, r.Retained3d, "3д — u2 и u3")
	assert.Equal(t, 2, r.Retained7d, "7д — u2 и u3")
	assert.Equal(t, 1, r.Retained14d, "14д — только u3")
	require.NotNil(t, r.GiveawayEndAt, "розыгрыш закончился — есть время итогов")
	// Итоги 5д назад: u1 уже ушёл (отпис 10д назад), u2 (отпис 2д назад) и u3 держались.
	assert.Equal(t, 2, r.RetainedAfterGiveaway, "на момент итогов держались u2 и u3")
}

// TestUTM_RetentionPoll — суточный поллинг: добор уже-состоящих и фиксация
// тихих отписок.
func TestUTM_RetentionPoll(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	owner, clicker := int64(300), int64(301)
	require.NoError(t, s.UpsertUser(ctx, User{UserID: owner}))
	require.NoError(t, s.UpsertUser(ctx, User{UserID: clicker}))
	ch, err := s.AddChannel(ctx, Channel{OwnerUserID: owner, ChatID: -300, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	linkID, err := s.CreateUTMLink(ctx, owner, ch.ID, "Опрос", "referral", "Опрос",
		"", "", "", "pollcode1", 0, 0)
	require.NoError(t, err)
	require.NoError(t, s.UTMRecordClick(ctx, linkID, clicker))

	// Работа поллинга: юзер атрибутирован, записи нет.
	work, err := s.ListUTMRetentionWork(ctx, 100)
	require.NoError(t, err)
	require.Len(t, work, 1, "кликнувший без записи — кандидат на проверку")
	assert.Equal(t, linkID, work[0].LinkID)
	assert.Equal(t, clicker, work[0].UserID)
	assert.Equal(t, int64(-300), work[0].ChatID)
	assert.False(t, work[0].HasRow)

	// Поллинг нашёл подписку (уже-состоящий, user_added не приходил).
	require.NoError(t, s.ApplyUTMRetentionCheck(ctx, work[0], true))
	r, err := s.GetUTMLinkRetention(ctx, linkID)
	require.NoError(t, err)
	assert.Equal(t, 1, r.Subscribed, "уже-состоящий добран поллингом")
	assert.Equal(t, 1, r.StillIn)

	// Через день поллинг увидел отписку.
	require.NoError(t, s.ApplyUTMRetentionCheck(ctx, work[0], false))
	r, err = s.GetUTMLinkRetention(ctx, linkID)
	require.NoError(t, err)
	assert.Equal(t, 0, r.StillIn, "тихая отписка зафиксирована")
	assert.Equal(t, 1, r.Subscribed)
}
