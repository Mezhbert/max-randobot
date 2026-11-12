package webapp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
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
