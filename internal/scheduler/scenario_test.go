package scheduler

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// makeSchedulerBot — создаёт giveaway в scheduled-статусе с каналом и publish_at в прошлом.
func setupScheduledGiveaway(t *testing.T, s *store.Store, ownerID int64) (store.Giveaway, store.Channel) {
	t.Helper()
	ctx := context.Background()
	testutil.EnsureUser(t, s, ownerID, "")

	// Канал (бот админ).
	ch, err := s.AddChannel(ctx, store.Channel{
		OwnerUserID: ownerID, ChatID: 9001, Title: "Канал", Link: "l", IsPublic: true, IsBotAdmin: true,
	})
	require.NoError(t, err)

	// Giveaway: scheduled, publish_at в прошлом.
	past := time.Now().UTC().Add(-time.Hour)
	_, err = s.Pool().Exec(ctx, `
		INSERT INTO giveaways (owner_user_id, name, post_text, button_text, publish_channel_ids, status, publish_at, finish_at, winners_count)
		VALUES ($1,'T','Текст поста','Участвовать',ARRAY[$2]::bigint[],'scheduled',$3,$4,1)
	`, ownerID, ch.ID, past, past.Add(2*time.Hour))
	require.NoError(t, err)

	items, err := s.ScheduledReady(ctx, time.Now().UTC())
	require.NoError(t, err)
	require.NotEmpty(t, items)
	return items[0], ch
}

// TestPublishPending_NoDuplicatesUnderConcurrentTicks — ГЛАВНЫЙ фикс:
// два параллельных PublishPending (как два тика/cron) публикуют пост ОДИН раз.
func TestPublishPending_NoDuplicatesUnderConcurrentTicks(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	mock := testutil.NewMock()
	sch := New(mock, s, nil)

	g, ch := setupScheduledGiveaway(t, s, 5000)

	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = sch.PublishPending(context.Background())
		}()
	}
	wg.Wait()

	// Пост отправлен в канал ровно один раз.
	toChat := 0
	for _, r := range mock.SnapshotSends() {
		if r.Method == "SendToChat" && r.ChatID == ch.ChatID {
			toChat++
		}
	}
	assert.Equal(t, 1, toChat, "пост должен быть опубликован ровно один раз (было: дубли при гонке тиков)")

	// Статус перешёл в active.
	got, err := s.GetGiveawayAny(context.Background(), g.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusActive, got.Status)
	// mid сохранён.
	require.Contains(t, got.PublishMessageIDs, ch.ChatID)
}

// TestFinishPending_NoDuplicateWinnerMessages — победитель получает ЛС ОДИН раз,
// даже если finish вызывается несколько раз (гонка тиков / повторный прогон после краха).
func TestFinishPending_NoDuplicateWinnerMessages(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	mock := testutil.NewMock()
	sch := New(mock, s, nil)
	ctx := context.Background()

	g, ch := setupScheduledGiveaway(t, s, 5100)
	// Участник.
	testutil.EnsureUser(t, s, 7777, "winner")
	_, err := s.Pool().Exec(ctx,
		`INSERT INTO participants (giveaway_id, user_id, tickets) VALUES ($1,$2,1)`, g.ID, 7777)
	require.NoError(t, err)
	// Поставим finish_at в прошлое и статус active (уже опубликован).
	_, err = s.Pool().Exec(ctx,
		`UPDATE giveaways SET status='active', finish_at=$2, winners_message='Заберите приз!' WHERE id=$1`,
		g.ID, time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)
	_ = ch

	// Имитируем сохранённый publish_message_id (для ветки итогов отдельным постом).
	require.NoError(t, s.StorePublishMessageID(ctx, g.ID, ch.ChatID, "mid.orig.1"))

	// Первый прогон finish.
	require.NoError(t, sch.FinishPending(ctx))
	notifyAfter1 := countSendsToUser(mock, 7777)
	assert.GreaterOrEqual(t, notifyAfter1, 1, "победитель должен получить ЛС")

	// Принудительно откатим статус обратно в active (имитация «следующий тик подобрал,
	// но прошлый прогон не успел зафиксировать finished»), и вызовем finish снова.
	_, err = s.Pool().Exec(ctx,
		`UPDATE giveaways SET status='active' WHERE id=$1`, g.ID)
	require.NoError(t, err)
	require.NoError(t, sch.FinishPending(ctx))

	// ЛС победителю НЕ должно было прийти повторно (идемпотентность через winners_notified_user_ids).
	notifyAfter2 := countSendsToUser(mock, 7777)
	assert.Equal(t, notifyAfter1, notifyAfter2,
		"повторный прогон finish не должен слать ЛС повторно: было %d, стало %d", notifyAfter1, notifyAfter2)

	// Итоговый статус — finished.
	got, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusFinished, got.Status)
}

// TestFinish_ResultsInOriginalPost_EditsPublishedPost — ветка «в исходном посте»:
// вызывается EditChatText с правильным mid, отдельный пост НЕ отправляется.
func TestFinish_ResultsInOriginalPost_EditsPublishedPost(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	mock := testutil.NewMock()
	sch := New(mock, s, nil)
	ctx := context.Background()

	g, ch := setupScheduledGiveaway(t, s, 5200)
	testutil.EnsureUser(t, s, 8888, "win")
	_, err := s.Pool().Exec(ctx,
		`INSERT INTO participants (giveaway_id, user_id, tickets) VALUES ($1,$2,1)`, g.ID, 8888)
	require.NoError(t, err)
	origMID := "mid.orig.2"
	require.NoError(t, s.StorePublishMessageID(ctx, g.ID, ch.ChatID, origMID))

	_, err = s.Pool().Exec(ctx, `
		UPDATE giveaways SET status='active', finish_at=$2,
		  results_in_original_post=TRUE, results_post_text='Финал!'
		WHERE id=$1`, g.ID, time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)

	require.NoError(t, sch.FinishPending(ctx))

	edits := mock.SnapshotEdits()
	require.Len(t, edits, 1, "должен быть один edit исходного поста")
	assert.Equal(t, origMID, edits[0].MessageID, "edit должен быть по mid исходного поста")
	// При results_in_original_post оригинальный текст сохраняется, итоги дописываются.
	assert.Contains(t, edits[0].Text, "Текст поста", "оригинальный текст должен сохраниться")
	assert.Contains(t, edits[0].Text, "🏁 Итоги!")
	assert.Contains(t, edits[0].Text, "Победители (1)")

	// Отдельный пост с итогами НЕ отправлялся.
	for _, r := range mock.SnapshotSends() {
		if r.Method == "SendToChat" && r.ChatID == ch.ChatID && strings.Contains(r.Text, "Итоги") {
			t.Fatalf("итоги не должны отправляться отдельным постом при results_in_original_post")
		}
	}
}

// TestFinish_EditFails_FallbackToSeparatePost — если EditMessage падает (напр.
// истёк лимит времени редактирования), итоги публикуются отдельным постом.
func TestFinish_EditFails_FallbackToSeparatePost(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	mock := testutil.NewMock()
	mock.EditErr = func(string) error { return errEdit } // эдит всегда падает
	sch := New(mock, s, nil)
	ctx := context.Background()

	g, ch := setupScheduledGiveaway(t, s, 5300)
	testutil.EnsureUser(t, s, 9999, "w")
	_, err := s.Pool().Exec(ctx,
		`INSERT INTO participants (giveaway_id, user_id, tickets) VALUES ($1,$2,1)`, g.ID, 9999)
	require.NoError(t, err)
	require.NoError(t, s.StorePublishMessageID(ctx, g.ID, ch.ChatID, "mid.orig.3"))
	_, err = s.Pool().Exec(ctx, `
		UPDATE giveaways SET status='active', finish_at=$2, results_in_original_post=TRUE
		WHERE id=$1`, g.ID, time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)

	require.NoError(t, sch.FinishPending(ctx))

	// Edit был attempted (1 раз) и упал.
	assert.Equal(t, 1, mock.EditCount())
	// Fallback: итоги отправлены отдельным постом в канал.
	separateResults := 0
	for _, r := range mock.SnapshotSends() {
		if r.Method == "SendToChat" && r.ChatID == ch.ChatID && strings.Contains(r.Text, "Итоги") {
			separateResults++
		}
	}
	assert.Equal(t, 1, separateResults, "при ошибке edit итоги должны уйти отдельным постом")

	// И сохранён results mid.
	got, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	require.Contains(t, got.ResultsMessageIDs, ch.ChatID, "results mid должен сохраниться")
}

// TestFinish_IdempotentResults — повторный прогон (после отката статуса) не
// дублирует публикацию итогов.
func TestFinish_IdempotentResults(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	mock := testutil.NewMock()
	sch := New(mock, s, nil)
	ctx := context.Background()

	g, ch := setupScheduledGiveaway(t, s, 5400)
	testutil.EnsureUser(t, s, 1111, "x")
	_, err := s.Pool().Exec(ctx,
		`INSERT INTO participants (giveaway_id, user_id, tickets) VALUES ($1,$2,1)`, g.ID, 1111)
	require.NoError(t, err)
	require.NoError(t, s.StorePublishMessageID(ctx, g.ID, ch.ChatID, "mid.o"))
	_, err = s.Pool().Exec(ctx, `
		UPDATE giveaways SET status='active', finish_at=$2,
		  results_in_original_post=FALSE, results_post_text='Результаты!'
		WHERE id=$1`, g.ID, time.Now().UTC().Add(-time.Minute))
	require.NoError(t, err)

	require.NoError(t, sch.FinishPending(ctx))
	resultsPosts1 := countResultsPosts(mock, ch.ChatID)

	// Откат статуса и повтор.
	_, err = s.Pool().Exec(ctx, `UPDATE giveaways SET status='active', results_published=FALSE WHERE id=$1`, g.ID)
	require.NoError(t, err)
	require.NoError(t, sch.FinishPending(ctx))
	resultsPosts2 := countResultsPosts(mock, ch.ChatID)

	// Должны были добавиться итоги второй раз — НО результаты уже были опубликованы
	// и results_message_ids заполнены, поэтому StoreResultsMessageID дедуплит.
	// Проверяем что второй прогон НЕ добавил дубль поста (защита по results_message_ids).
	assert.Equal(t, resultsPosts1, resultsPosts2,
		"повторный прогон не должен дублировать пост итогов: %d vs %d", resultsPosts1, resultsPosts2)
}

// --- helpers ---

var errEdit = &simpleErr{"edit failed: message too old"}

type simpleErr struct{ msg string }

func (e *simpleErr) Error() string { return e.msg }

func countSendsToUser(m *testutil.MockBotAPI, userID int64) int {
	n := 0
	for _, r := range m.SnapshotSends() {
		if r.UserID == userID {
			n++
		}
	}
	return n
}

func countResultsPosts(m *testutil.MockBotAPI, chatID int64) int {
	n := 0
	for _, r := range m.SnapshotSends() {
		if r.Method == "SendToChat" && r.ChatID == chatID && strings.Contains(r.Text, "Итоги") {
			n++
		}
	}
	return n
}

// TestPublish_RetryCapCancelsAfter10Failures — недоступный канал (SendToChat
// падает): после 10 попыток розыгрыш cancelled + владелец уведомлён,
// а не бесконечный цикл ретраев.
func TestPublish_RetryCapCancelsAfter10Failures(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	mock := testutil.NewMock()
	sch := New(mock, s, nil)
	ctx := context.Background()

	g, _ := setupScheduledGiveaway(t, s, 5010)

	// Канал недоступен (бот кикнут): SendToChat всегда падает.
	mock.SendToChatMID = func(chatID int64) (string, error) {
		return "", errors.New("chat.not.found")
	}

	for i := 0; i < 9; i++ {
		_ = sch.PublishPending(ctx)
	}
	after9, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusScheduled, after9.Status, "до 10 попыток — всё ещё scheduled")
	assert.Equal(t, 9, after9.PublishAttempts)

	_ = sch.PublishPending(ctx) // 10-я
	after10, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, store.StatusCancelled, after10.Status, "после 10 неудач — cancelled")
	assert.Equal(t, 10, after10.PublishAttempts)

	notified := false
	for _, r := range mock.SnapshotSends() {
		if r.UserID == g.OwnerUserID && strings.Contains(r.Text, "Не удалось опубликовать") {
			notified = true
		}
	}
	assert.True(t, notified, "владелец должен получить объяснение")

	// Дальше тикер молчит: больше не scheduled.
	_ = sch.PublishPending(ctx)
	after11, err := s.GetGiveawayAny(ctx, g.ID)
	require.NoError(t, err)
	assert.Equal(t, 10, after11.PublishAttempts, "после cancel счётчик не растёт")
}
