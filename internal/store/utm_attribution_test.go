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
	linkID, err := s.CreateUTMLink(ctx, owner, ch.ID, "Реклама у Иванова", "referral", "Реклама у Иванова", "", "ab12cd34ef", 1500)
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
