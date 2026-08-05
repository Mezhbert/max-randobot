package store

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClaimForPublish_Race — ядро фикса дублей: две горутины пытаются
// захватить один scheduled-розыгрыш. Выигрывает ровно одна, вторая
// получает ErrNotFound.
func TestClaimForPublish_Race(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	g := s.freshGiveaway(ctx, t, 1000, StatusScheduled)

	var (
		wg         sync.WaitGroup
		claimed    int
		notFound   int
		mu         sync.Mutex
	)
	for i := 0; i < 5; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := s.ClaimForPublish(ctx, g.ID)
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				claimed++
			} else if errors.Is(err, ErrNotFound) {
				notFound++
			} else {
				t.Errorf("неожиданная ошибка claim: %v", err)
			}
		}()
	}
	wg.Wait()

	assert.Equal(t, 1, claimed, "ровно один должен захватить")
	assert.Equal(t, 4, notFound, "остальные должны получить ErrNotFound")
}

func TestClaimForFinish_OnlyActiveFinished(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	// Розыгрыш в active — можно захватить.
	g := s.freshGiveaway(ctx, t, 1000, StatusActive)
	claimed, err := s.ClaimForFinish(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusFinishing, claimed.Status)

	// Повторно — уже не active, а finishing → ErrNotFound.
	_, err = s.ClaimForFinish(ctx, g.ID)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestRevertPublish_BackToScheduled(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	g := s.freshGiveaway(ctx, t, 1000, StatusScheduled)
	_, err := s.ClaimForPublish(ctx, g.ID)
	require.NoError(t, err)

	require.NoError(t, s.RevertPublish(ctx, g.ID))

	// Снова доступен для захвата.
	_, err = s.ClaimForPublish(ctx, g.ID)
	assert.NoError(t, err)
}

func TestMarkPublished_Active(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	g := s.freshGiveaway(ctx, t, 1000, StatusScheduled)
	_, _ = s.ClaimForPublish(ctx, g.ID)
	require.NoError(t, s.MarkPublished(ctx, g.ID))

	got, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusActive, got.Status)
}

func TestMarkFinished_FinishedAndResultsPublished(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	g := s.freshGiveaway(ctx, t, 1000, StatusActive)
	_, _ = s.ClaimForFinish(ctx, g.ID)
	require.NoError(t, s.MarkFinished(ctx, g.ID))

	got, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, StatusFinished, got.Status)
	assert.True(t, got.ResultsPublished, "итоги должны быть помечены опубликованными")
}

func TestListStaleClaimed(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	// Создаём «зависший» finishing-розыгрыш: статус ставим в finishing,
	// затем сдвигаем updated_at в прошлое. Триггер touch_updated_at безусловно
	// обновляет updated_at на now() при любом UPDATE, поэтому отключаем его
	// на момент сдвига и включаем обратно (тестовая схема).
	g := s.freshGiveaway(ctx, t, 1000, StatusActive)
	_, err := s.pool.Exec(ctx,
		`UPDATE giveaways SET status='finishing'::giveaway_status WHERE id=$1`, g.ID)
	require.NoError(t, err)

	_, err = s.pool.Exec(ctx, `ALTER TABLE giveaways DISABLE TRIGGER trg_giveaways_touch`)
	require.NoError(t, err)
	_, err = s.pool.Exec(ctx,
		`UPDATE giveaways SET updated_at = now() - interval '10 minutes' WHERE id=$1`, g.ID)
	require.NoError(t, err)
	_, err = s.pool.Exec(ctx, `ALTER TABLE giveaways ENABLE TRIGGER trg_giveaways_touch`)
	require.NoError(t, err)

	items, err := s.ListStaleClaimed(ctx, time.Now().UTC().Add(-5*time.Minute), 100)
	require.NoError(t, err)
	found := false
	for _, it := range items {
		if it.ID == g.ID {
			found = true
			assert.Equal(t, StatusFinishing, it.Status)
		}
	}
	assert.True(t, found, "зависший розыгрыш должен найтись среди stale")
}

// TestFinishIdempotence_Notified — идемпотентность уведомления победителей:
// MarkWinnersNotified накапливает, повторная отметка не дублирует.
func TestMarkWinnersNotified_Idempotent(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	g := s.freshGiveaway(ctx, t, 1000, StatusActive)

	require.NoError(t, s.MarkWinnersNotified(ctx, g.ID, []int64{1, 2}))
	require.NoError(t, s.MarkWinnersNotified(ctx, g.ID, []int64{2, 3})) // 2 уже есть

	got, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.ElementsMatch(t, []int64{1, 2, 3}, got.WinnersNotifiedUserIDs)
}

// TestScanGiveaway_NewColumns — новые колонки (winners_notified_user_ids,
// results_published) читаются корректно после миграции 0004.
func TestScanGiveaway_NewColumns(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	g := s.freshGiveaway(ctx, t, 1000, StatusActive)
	_, err := s.pool.Exec(ctx,
		`UPDATE giveaways SET winners_notified_user_ids='{100,200}', results_published=TRUE WHERE id=$1`, g.ID)
	require.NoError(t, err)

	got, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, []int64{100, 200}, got.WinnersNotifiedUserIDs)
	assert.True(t, got.ResultsPublished)
}

// TestListParticipantsForDraw — участники с билетами для weighted-выбора.
func TestListParticipantsForDraw(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	g := s.freshGiveaway(ctx, t, 1000, StatusActive)
	// Добавляем участников с разными весами. Сначала пользователи (FK на users).
	for _, uid := range []int64{101, 102, 103} {
		_, err := s.pool.Exec(ctx, `INSERT INTO users (user_id) VALUES ($1) ON CONFLICT DO NOTHING`, uid)
		require.NoError(t, err)
	}
	for _, p := range []struct {
		uid  int64
		tick int
	}{
		{101, 1}, {102, 5}, {103, 3},
	} {
		_, err := s.pool.Exec(ctx,
			`INSERT INTO participants (giveaway_id, user_id, tickets) VALUES ($1, $2, $3)`,
			g.ID, p.uid, p.tick)
		require.NoError(t, err)
	}

	entries, err := s.ListParticipantsForDraw(ctx, g.ID)
	require.NoError(t, err)
	assert.Len(t, entries, 3)

	byUser := map[int64]int{}
	for _, e := range entries {
		byUser[e.UserID] = e.Tickets
	}
	assert.Equal(t, 1, byUser[101])
	assert.Equal(t, 5, byUser[102])
	assert.Equal(t, 3, byUser[103])
}

// TestStorePublishMessageID_Idempotent — сохранение mid для канала.
func TestStorePublishMessageID(t *testing.T) {
	s, cleanup := newTestStore(t)
	defer cleanup()
	ctx := context.Background()

	g := s.freshGiveaway(ctx, t, 1000, StatusActive)
	require.NoError(t, s.StorePublishMessageID(ctx, g.ID, 555, "mid.abc.123"))
	require.NoError(t, s.StorePublishMessageID(ctx, g.ID, 666, "mid.def.456"))

	got, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, "mid.abc.123", got.PublishMessageIDs[555])
	assert.Equal(t, "mid.def.456", got.PublishMessageIDs[666])
}

func ExampleStore_statusFlow() {
	// Документирующий пример потока статусов publish.
	fmt.Println(StatusScheduled, "->", StatusPublishing, "->", StatusActive)
	// Output: scheduled -> publishing -> active
}
