package webapp

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
	"testing"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/testutil"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeCaptcha — детерминированная капча для тестов: верный ответ всегда "1234".
type fakeCaptcha struct{}

func (fakeCaptcha) GenerateForKey(key string) ([]byte, error) { return []byte("png"), nil }
func (fakeCaptcha) VerifyForKey(key, answer string) bool      { return answer == "1234" }

// TestParticipateAPI_InitDataInBody — регрессия РЕСТАВРАЦИИ мини-аппа: init_data
// передаётся В ТЕЛЕ POST-запроса. Ранняя версия страницы слала POST без
// init_data, а сервер читал только query/header → каждый запрос участия падал
// в 401 («мини-апп-путь не взлетел у клиента», авг. 2026).
func TestParticipateAPI_InitDataInBody(t *testing.T) {
	called := false
	d := &Deps{
		BotToken: "", // dev-режим: подпись не проверяется, user_id=0
		Participate: func(giveawayID, userID int64) ParticipateResult {
			called = true
			return ParticipateResult{Ok: true, Notifiable: true}
		},
	}
	h := Handler(d, nil)

	// init_data в теле → авторизован (не 401).
	req := httptest.NewRequest("POST", "/app/api/participate",
		strings.NewReader(`{"giveaway_id":5,"init_data":"user=%7B%22id%22%3A1%7D&auth_date=1&hash=abc"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code,
		"init_data в теле POST должна авторизовывать (старый баг — 401 на каждый запрос)")
	assert.True(t, called, "Participate должен быть вызван")
	assert.Contains(t, rec.Body.String(), `"notifiable":true`)

	// Совсем без init_data → 401.
	req2 := httptest.NewRequest("POST", "/app/api/participate",
		strings.NewReader(`{"giveaway_id":5}`))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	assert.Equal(t, http.StatusUnauthorized, rec2.Code)
}

// TestCaptchaVerifyAPI_GatesRegistration — P3: API капчи мини-аппа
// регистрирует участника ТОЛЬКО после верного ответа; неверный ответ —
// ok:false без вызова Participate.
func TestCaptchaVerifyAPI_GatesRegistration(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, s.UpsertUser(ctx, store.User{UserID: 1, FirstName: "Owner"}))
	var gid int64
	err := s.Pool().QueryRow(ctx, `
		INSERT INTO giveaways (owner_user_id, name, post_text, button_text, status,
			finish_mode, finish_participants_count, winners_count, captcha_mode)
		VALUES (1, 'T', 'пост', 'Участвовать', 'active', 'count', 1000, 1, 'all')
		RETURNING id
	`).Scan(&gid)
	require.NoError(t, err)

	registrations := 0
	d := &Deps{
		DB:        s,
		BotToken:  "", // dev-режим
		Captcha:   fakeCaptcha{},
		Participate: func(giveawayID, userID int64) ParticipateResult {
			registrations++
			return ParticipateResult{Ok: true}
		},
	}
	h := Handler(d, nil)

	post := func(body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest("POST", "/app/api/captcha/verify", strings.NewReader(body))
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	// Неверный ответ — регистрации нет.
	rec := post(`{"giveaway_id":` + itoa(gid) + `,"answer":"0000","init_data":"hash=abc"}`)
	assert.Contains(t, rec.Body.String(), `"ok":false`)
	assert.Zero(t, registrations, "неверный ответ капчи не должен регистрировать")

	// Верный ответ ("1234" у fakeCaptcha) — регистрация.
	rec = post(`{"giveaway_id":` + itoa(gid) + `,"answer":"1234","init_data":"hash=abc"}`)
	assert.Contains(t, rec.Body.String(), `"ok":true`)
	assert.Equal(t, 1, registrations, "верный ответ регистрирует участника")
}

// TestGiveawayAPI_PublicData — публичный API данных розыгрыша отдаёт имя,
// статус, победителей и флаг капчи (для страниц участия/итогов).
func TestGiveawayAPI_PublicData(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, s.UpsertUser(ctx, store.User{UserID: 1, FirstName: "Owner"}))
	require.NoError(t, s.UpsertUser(ctx, store.User{UserID: 42, FirstName: "Победитель"}))
	var gid int64
	err := s.Pool().QueryRow(ctx, `
		INSERT INTO giveaways (owner_user_id, name, post_text, button_text, status,
			finish_mode, finish_participants_count, winners_count, captcha_mode,
			winners_user_ids, updated_at)
		VALUES (1, 'Приз', 'пост', 'Участвовать', 'finished', 'count', 1000, 1, 'all',
			ARRAY[42]::bigint[], now())
		RETURNING id
	`).Scan(&gid)
	require.NoError(t, err)

	d := &Deps{DB: s, BotUsername: "my_bot"}
	h := Handler(d, nil)

	req := httptest.NewRequest("GET", "/app/api/giveaway/"+itoa(gid), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, `"name":"Приз"`)
	assert.Contains(t, body, `"status":"finished"`)
	assert.Contains(t, body, `"captcha":true`)
	assert.Contains(t, body, "Победитель")
	assert.Contains(t, body, `"bot":"my_bot"`)

	// Несуществующий розыгрыш — 404.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("GET", "/app/api/giveaway/999", nil))
	assert.Equal(t, http.StatusNotFound, rec2.Code)
}

// itoa — обёртка для краткости в тестах.
func itoa(n int64) string { return strconv.FormatInt(n, 10) }

// TestTrackAPI_ClickMembershipCheck — клик по трекинг-ссылке: первичная
// проверка «уже участник?» уходит в очередь и фиксирует уже-состоящего
// (user_added для него не придёт никогда) с отдельным флагом.
func TestTrackAPI_ClickMembershipCheck(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	ctx := context.Background()

	require.NoError(t, s.UpsertUser(ctx, store.User{UserID: 1, FirstName: "Owner"}))
	require.NoError(t, s.UpsertUser(ctx, store.User{UserID: 42, FirstName: "Кликер"}))
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: 1, ChatID: -424242, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)
	linkID, err := s.CreateUTMLink(ctx, 1, ch.ID, "Проверка", "referral", "Проверка",
		"", "", "", "clickcheck", 0, 0, "unique")
	require.NoError(t, err)

	// Валидная подпись init_data по формуле SDK: данные — параметры,
	// отсортированные и склеенные \n (без url-кодирования), ключ —
	// HMAC_SHA256("WebAppData", token).
	token := "test-token"
	parts := []string{"auth_date=1", `user={"id":42}`}
	sort.Strings(parts)
	dataCheck := strings.Join(parts, "\n")
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	sign := hmac.New(sha256.New, secret.Sum(nil))
	sign.Write([]byte(dataCheck))
	initData := "auth_date=1&hash=" + hex.EncodeToString(sign.Sum(nil)) +
		"&user=" + url.QueryEscape(`{"id":42}`)

	d := &Deps{
		DB:       s,
		BotToken: token,
		// Очередь считает юзера 42 УЖЕ участником канала.
		CheckSubscriber: func(c context.Context, chatID, userID int64) (bool, error) {
			return userID == 42, nil
		},
	}
	h := Handler(d, nil)

	req := httptest.NewRequest("POST", "/app/api/track",
		strings.NewReader(`{"init_data":"`+initData+`","code":"clickcheck"}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	assert.Equal(t, http.StatusOK, rec.Code)

	// Очередь асинхронна — ждём запись подписчика (≤3с).
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		ret, err := s.GetUTMLinkRetention(ctx, linkID)
		require.NoError(t, err)
		if ret.Subscribed == 1 {
			assert.Equal(t, 1, ret.WasMemberAtClick, "уже-участник помечен отдельным флагом")
			assert.Equal(t, 1, ret.StillIn)
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("уже-участник не зафиксирован очередью за 3с")
}

// TestAnalyticsAPI_GiveawaysAndSendPost — раздел розыгрышей миниаппа:
// список с черновиками + отправка готового поста админу в чат.
func TestAnalyticsAPI_GiveawaysAndSendPost(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	ctx := context.Background()

	const owner = 5100
	require.NoError(t, s.UpsertUser(ctx, store.User{UserID: owner, FirstName: "Owner"}))
	var draftID, activeID int64
	require.NoError(t, s.Pool().QueryRow(ctx, `
		INSERT INTO giveaways (owner_user_id, name, post_text, button_text, status, draft_step, finish_mode)
		VALUES ($1, 'Черновик про носки', 'пост текст', 'Участвовать', 'draft', 'post', 'time')
		RETURNING id
	`, owner).Scan(&draftID))
	require.NoError(t, s.Pool().QueryRow(ctx, `
		INSERT INTO giveaways (owner_user_id, name, post_text, button_text, status, finish_mode)
		VALUES ($1, 'Активный розыгрыш', 'пост 2', 'Участвовать', 'active', 'count')
		RETURNING id
	`, owner).Scan(&activeID))

	// Валидная подпись init_data владельца (HMAC по формуле SDK).
	token := "test-token"
	initData := signedInitData(t, token, owner)

	sent := false
	d := &Deps{
		DB:       s,
		BotToken: token,
		SendPost: func(c context.Context, uid int64, g store.Giveaway) error {
			sent = true
			if uid != owner || g.ID != draftID {
				t.Fatalf("send-post: неверный получатель/розыгрыш: uid=%d gw=%d", uid, g.ID)
			}
			return nil
		},
	}
	h := Handler(d, nil)

	// Список: оба розыгрыша (включая черновик), новые первыми.
	req := httptest.NewRequest("GET", "/app/api/analytics/giveaways?init_data="+url.QueryEscape(initData), nil)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	assert.Contains(t, body, "Черновик про носки")
	assert.Contains(t, body, "Активный розыгрыш")
	assert.Contains(t, body, `"status":"draft"`)

	// Отправка поста админу.
	req2 := httptest.NewRequest("POST", "/app/api/analytics/send-post?init_data="+url.QueryEscape(initData),
		strings.NewReader(`{"giveaway_id":`+strconv.FormatInt(draftID, 10)+`}`))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	require.Equal(t, http.StatusOK, rec2.Code)
	assert.Contains(t, rec2.Body.String(), `"ok":true`)
	assert.True(t, sent, "SendPost должен быть вызван")

	// Чужой розыгрыш — 404.
	req3 := httptest.NewRequest("POST", "/app/api/analytics/send-post",
		strings.NewReader(`{"giveaway_id":`+strconv.FormatInt(activeID, 10)+`}`))
	// другому юзеру недоступно без его init_data — здесь owner и так свой; проверим несуществующий:
	req4 := httptest.NewRequest("POST", "/app/api/analytics/send-post?init_data="+url.QueryEscape(initData),
		strings.NewReader(`{"giveaway_id":99999}`))
	rec4 := httptest.NewRecorder()
	h.ServeHTTP(rec4, req4)
	assert.Equal(t, http.StatusNotFound, rec4.Code)
	_ = req3
}


// signedInitData — валидная init_data для userID по формуле SDK
// (параметры сортируются и склеиваются \n, ключ HMAC("WebAppData", token)).
func signedInitData(t *testing.T, token string, userID int64) string {
	t.Helper()
	parts := []string{"auth_date=1", fmt.Sprintf(`user={"id":%d}`, userID)}
	sort.Strings(parts)
	dataCheck := strings.Join(parts, "\n")
	secret := hmac.New(sha256.New, []byte("WebAppData"))
	secret.Write([]byte(token))
	sign := hmac.New(sha256.New, secret.Sum(nil))
	sign.Write([]byte(dataCheck))
	return "auth_date=1&hash=" + hex.EncodeToString(sign.Sum(nil)) +
		"&user=" + url.QueryEscape(fmt.Sprintf(`{"id":%d}`, userID))
}
