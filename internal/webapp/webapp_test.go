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

// TestAppAPI_CreateGiveawayAndTracking — транспорт действий миниаппа:
// JSON → замыкание CreateGiveaway/CreateTracking → ответ; валидация полей
// покрыта в handlers-тестах, тут проверяем API-слой (auth, формат, ошибки).
func TestAppAPI_CreateGiveawayAndTracking(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	ctx := context.Background()

	const owner = 5200
	require.NoError(t, s.UpsertUser(ctx, store.User{UserID: owner, FirstName: "Owner"}))
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: owner, ChatID: -52100, Title: "Канал", IsPublic: true, IsBotAdmin: true})
	require.NoError(t, err)

	token := "test-token"
	initData := signedInitData(t, token, owner)

	var gotPost string
	var gotTrack CreateTrackingReq
	d := &Deps{
		DB:       s,
		BotToken: token,
		CreateGiveaway: func(c context.Context, uid int64, in CreateGiveawayReq) (int64, string, error) {
			gotPost = in.PostText
			if in.PostText == "" {
				return 0, "не заполнен пост", nil
			}
			return 42, "", nil
		},
		CreateTracking: func(c context.Context, uid int64, in CreateTrackingReq) ([]string, string, error) {
			gotTrack = in
			if in.Name == "" {
				return nil, "введите название", nil
			}
			return []string{"abc123"}, "", nil
		},
	}
	h := Handler(d, nil)
	qinit := "?init_data=" + url.QueryEscape(initData)

	// Создание розыгрыша: поля доходят, ok=true, id на месте.
	req := httptest.NewRequest("POST", "/app/api/giveaways/create"+qinit,
		strings.NewReader(`{"post_text":"Пост из миниаппа","pub_channels":[`+strconv.FormatInt(ch.ID, 10)+`],"winners":3}`))
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	require.Equal(t, http.StatusOK, rec.Code)
	assert.Contains(t, rec.Body.String(), `"ok":true`)
	assert.Contains(t, rec.Body.String(), `"giveaway_id":42`)
	assert.Equal(t, "Пост из миниаппа", gotPost)

	// Без init_data — 401.
	reqAnon := httptest.NewRequest("POST", "/app/api/giveaways/create",
		strings.NewReader(`{"post_text":"x"}`))
	recAnon := httptest.NewRecorder()
	h.ServeHTTP(recAnon, reqAnon)
	assert.Equal(t, http.StatusUnauthorized, recAnon.Code)

	// Пустой пост — авторская ошибка, ok=false.
	req2 := httptest.NewRequest("POST", "/app/api/giveaways/create"+qinit,
		strings.NewReader(`{"post_text":"","pub_channels":[1]}`))
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, req2)
	assert.Contains(t, rec2.Body.String(), `"ok":false`)
	assert.Contains(t, rec2.Body.String(), "пост")

	// Трекинг: поля доходят, коды в ответе.
	req3 := httptest.NewRequest("POST", "/app/api/tracking/create"+qinit,
		strings.NewReader(`{"mode":"ads","channel_id":`+strconv.FormatInt(ch.ID, 10)+`,"name":"Реклама","cost":1500,"link_type":"super","post_ref":"https://max.ru/k/42"}`))
	rec3 := httptest.NewRecorder()
	h.ServeHTTP(rec3, req3)
	assert.Contains(t, rec3.Body.String(), `"ok":true`)
	assert.Contains(t, rec3.Body.String(), `"abc123"`)
	assert.Equal(t, "super", gotTrack.LinkType)
	assert.InDelta(t, 1500.0, gotTrack.Cost, 0.01)

	// Трекинг без названия — авторская ошибка.
	req4 := httptest.NewRequest("POST", "/app/api/tracking/create"+qinit,
		strings.NewReader(`{"mode":"ads","channel_id":`+strconv.FormatInt(ch.ID, 10)+`,"name":""}`))
	rec4 := httptest.NewRecorder()
	h.ServeHTTP(rec4, req4)
	assert.Contains(t, rec4.Body.String(), `"ok":false`)
	assert.Contains(t, rec4.Body.String(), "название")
}

// futureMSK — дата через N часов в формате ДД.ММ.ГГГГ ЧЧ:ММ (МСК).
func futureMSK(hours int) string {
	return time.Now().Add(time.Duration(hours) * time.Hour).In(msk()).Format("02.01.2006 15:04")
}

// TestAnalyticsAPI_DeleteLink — удаление ссылки со всей статистикой:
// только владелец; клики/подписчики/атрибуции/сама ссылка исчезают,
// соседние ссылки не задеты.
func TestAnalyticsAPI_DeleteLink(t *testing.T) {
	s, cleanup := testutil.NewTestStore(t)
	defer cleanup()
	ctx := context.Background()

	const owner, other = 5300, 5301
	require.NoError(t, s.UpsertUser(ctx, store.User{UserID: owner, FirstName: "Owner"}))
	require.NoError(t, s.UpsertUser(ctx, store.User{UserID: other, FirstName: "Other"}))
	require.NoError(t, s.UpsertUser(ctx, store.User{UserID: 7002, FirstName: "Кликер"}))
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: owner, ChatID: -53100, Title: "Ch", IsBotAdmin: true})
	require.NoError(t, err)

	keepID, err := s.CreateUTMLink(ctx, owner, ch.ID, "Остающаяся", "referral", "Остающаяся", "", "", "", "keepcode1", 0, 0, "unique")
	require.NoError(t, err)
	delID, err := s.CreateUTMLink(ctx, owner, ch.ID, "Удаляемая", "referral", "Удаляемая", "", "", "", "delcode1", 100, 0, "unique")
	require.NoError(t, err)

	// Данные удаляемой ссылки: клик + подписчик + атрибуция.
	require.NoError(t, s.UTMRecordClick(ctx, delID, 7002))
	require.NoError(t, s.UpsertUTMSubscriber(ctx, delID, 7002, ch.ChatID))
	// И то же для остающейся — должна быть нетронута.
	require.NoError(t, s.UTMRecordClick(ctx, keepID, 7002))

	token := "test-token"
	ownerInit := signedInitData(t, token, owner)
	otherInit := signedInitData(t, token, other)
	d := &Deps{DB: s, BotToken: token}
	h := Handler(d, nil)

	// Чужой юзер — 404.
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, httptest.NewRequest("POST", "/app/api/analytics/link/delete?init_data="+url.QueryEscape(otherInit),
		strings.NewReader(`{"link_id":`+strconv.FormatInt(delID, 10)+`}`)))
	assert.Equal(t, http.StatusNotFound, rec.Code)

	// Владелец — удаляет.
	rec2 := httptest.NewRecorder()
	h.ServeHTTP(rec2, httptest.NewRequest("POST", "/app/api/analytics/link/delete?init_data="+url.QueryEscape(ownerInit),
		strings.NewReader(`{"link_id":`+strconv.FormatInt(delID, 10)+`}`)))
	require.Equal(t, http.StatusOK, rec2.Code)
	assert.Contains(t, rec2.Body.String(), `"ok":true`)

	// Ссылка и её данные исчезли; соседняя цела.
	_, err = s.GetUTMLinkByCode(ctx, "delcode1")
	assert.Error(t, err, "удаляемая ссылка исчезла")
	keep, err := s.GetUTMLinkByCode(ctx, "keepcode1")
	require.NoError(t, err)
	assert.Equal(t, int64(1), keep.Clicks, "соседняя ссылка не задета")

	var delClicks, delSubs, delAttr int
	_ = s.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM utm_clicks WHERE utm_link_id=$1`, delID).Scan(&delClicks)
	_ = s.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM utm_subscribers WHERE utm_link_id=$1`, delID).Scan(&delSubs)
	_ = s.Pool().QueryRow(ctx, `SELECT COUNT(*) FROM user_utm_attribution WHERE utm_link_id=$1`, delID).Scan(&delAttr)
	assert.Zero(t, delClicks, "клики удаляемой ссылки стёрты")
	assert.Zero(t, delSubs, "подписчики удалены")
	assert.Zero(t, delAttr, "атрибуции удалены")
}
