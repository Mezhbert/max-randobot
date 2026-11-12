// Package webapp — мини-апп бота.
//
// Назначения:
//  1. ГЛАВНОЕ: бесшовный трекинг-редиректор. Ссылка вида
//     https://max.ru/<bot>?startapp=u_<code> открывает ЭТУ страницу как
//     мини-апп БЕЗ запуска бота (официальная механика MAX, см. dev.max.ru
//     /docs/webapps/introduction): клиент передаёт подписанный user
//     (WebAppData), мы валидируем HMAC → фиксируем клик С user_id →
//     отвечаем, куда редиректить (канал/пост) → JS мгновенно ведёт юзера
//     туда. Юзер видит только короткий спиннер — чат бота не открывается.
//  2. Участие в розыгрыше (start_param=part_<id> → participate.html):
//     инфо о розыгрыше, капча (картинка + проверка на сервере) и регистрация
//     — всё в мини-аппе, без чата бота (P3/P5).
//  3. Результаты розыгрыша (start_param=results_<id> → results.html):
//     победители/статистика завершённого розыгрыша (P4/P5).
//  4. Главный экран-описание (если мини-апп открыт без start_param).
package webapp

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"strings"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
)

//go:embed app/*
var staticFS embed.FS

// Deps — зависимости мини-аппа. Передаётся указателем: BotUsername уточняется
// после старта (GetMyInfo) и мутируется в main.
type Deps struct {
	DB       *store.Store
	Captcha  CaptchaService // nil — капча в мини-аппе недоступна
	BotToken string         // для валидации init_data (пусто — dev-режим без проверки)

	// BotUsername — публичное имя бота для deep-link'ов (opt-in уведомлений
	// на странице участия). Мутируется из main после GetMyInfo.
	BotUsername string

	// Participate — участие из мини-аппа (полный пайплайн: статус → подписки
	// → регистрация, без сообщений от бота). Реализация в handlers.
	Participate func(giveawayID, userID int64) ParticipateResult
}

// CaptchaService — минимальный интерфейс капчи (совместим с internal/captcha).
// Ключ капчи мини-аппа — "<userID>:<giveawayID>" (не пересекается с чатовой
// капчей, ключ которой — просто userID).
type CaptchaService interface {
	GenerateForKey(key string) ([]byte, error)
	VerifyForKey(key, answer string) bool
}

// ParticipateResult — ответ API участия (зеркалит handlers.ParticipateResult;
// отдельный тип, чтобы webapp не зависел от пакета handlers).
type ParticipateResult struct {
	Ok         bool
	Err        string
	Notifiable bool
}

// Handler возвращает HTTP-хендлеры мини-аппа:
//
//	GET  /app/                        → статика (главный экран + редиректор)
//	GET  /app/participate.html        → страница участия (P3/P5)
//	GET  /app/results.html            → страница итогов (P4/P5)
//	POST /app/api/track               → клик: init_data → user_id → редирект-цель
//	GET  /app/api/giveaway/{id}       → JSON с данными розыгрыша
//	POST /app/api/participate         → регистрация участия (init_data-авторизация)
//	GET  /app/api/captcha/start       → PNG-капча (по init_data+giveaway_id)
//	POST /app/api/captcha/verify      → проверка ответа + регистрация участника
func Handler(d *Deps, log *slog.Logger) http.Handler {
	if log == nil {
		log = slog.Default()
	}
	mux := http.NewServeMux()

	// Статика мини-аппа.
	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/app/" || path == "/app" || path == "/app/index.html" {
			serve(w, "index.html")
			return
		}
		// Остальная статика из embed (participate/results/help); отсутствующие
		// страницы — на главную.
		name := path[len("/app/"):]
		data, err := staticFS.ReadFile("app/" + name)
		if err != nil {
			http.Redirect(w, r, "/app/index.html", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})

	// Трекинг-клик: {init_data, code} → {redirect}.
	// init_data валидируется HMAC-подписью (подделать нельзя); её отсутствие
	// (открыто в обычном браузере) — анонимный клик.
	mux.HandleFunc("/app/api/track", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"post only"}`, http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			InitData string `json:"init_data"`
			Code     string `json:"code"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil || req.Code == "" || len(req.Code) > 64 {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}

		link, err := d.DB.GetUTMLinkByCode(r.Context(), req.Code)
		if err != nil {
			writeJSON(w, map[string]any{"ok": false, "redirect": "/app/index.html"})
			return
		}

		userID := int64(0)
		if req.InitData != "" && d.BotToken != "" {
			if data, err := maxbot.ValidateInitData(req.InitData, d.BotToken); err == nil && data.User.ID > 0 {
				userID = data.User.ID
				// Юзер известен, но мог НИКОГДА не открывать бота — тогда его нет
				// в таблице users, а user_utm_attribution имеет FK на неё: клик
				// молча откатывался целиком (ни клика, ни атрибуции). Создаём.
				// (UserApp из init_data юзернейм не несёт — только имя.)
				_ = d.DB.UpsertUser(r.Context(), store.User{
					UserID:    data.User.ID,
					FirstName: data.User.FirstName,
				})
			}
		}

		if userID > 0 {
			// Идентифицированный клик: юзер известен → атрибуция подписки.
			if err := d.DB.UTMRecordClick(r.Context(), link.ID, userID); err != nil {
				log.Warn("track click", "err", err, "code", req.Code)
			}
		} else {
			if err := d.DB.RecordAnonymousClick(r.Context(), link.ID); err != nil {
				log.Warn("track anon click", "err", err, "code", req.Code)
			}
		}

		writeJSON(w, map[string]any{"ok": true, "redirect": trackTarget(r, d, link)})
	})

	// API: данные розыгрыша (страницы участия и итогов). Публичные поля —
	// без авторизации (название/статус/победители не секрет); init_data
	// опционально передаётся для персонализации в будущем.
	mux.HandleFunc("/app/api/giveaway/", func(w http.ResponseWriter, r *http.Request) {
		idStr := strings.TrimPrefix(r.URL.Path, "/app/api/giveaway/")
		gid, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || gid <= 0 {
			http.Error(w, `{"error":"bad id"}`, http.StatusBadRequest)
			return
		}
		g, err := d.DB.GetGiveawayAny(r.Context(), gid)
		if err != nil {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}

		// Победители с именами.
		type winner struct {
			Name string `json:"name"`
		}
		winners := make([]winner, 0, len(g.WinnersUserIDs))
		for _, wid := range g.WinnersUserIDs {
			name := fmt.Sprintf("Участник #%d", wid)
			if u, err := d.DB.GetUser(r.Context(), wid); err == nil && u.FirstName != "" {
				name = util.DisplayName(wid, u.Username, u.FirstName)
			}
			winners = append(winners, winner{Name: name})
		}

		participants, _ := d.DB.CountParticipants(r.Context(), g.ID)

		resp := struct {
			Name         string   `json:"name"`
			Status       string   `json:"status"`
			Participants int      `json:"participants"`
			WinnersCount int      `json:"winners_count"`
			Winners      []winner `json:"winners"`
			FinishedAt   string   `json:"finished_at,omitempty"`
			Captcha      bool     `json:"captcha"`       // нужна ли капча при участии
			Bot          string   `json:"bot,omitempty"` // username бота (deep-link opt-in)
		}{
			Name:         g.Name,
			Status:       g.Status,
			Participants: participants,
			WinnersCount: g.WinnersCount,
			Winners:      winners,
			Captcha:      g.CaptchaMode == store.CaptchaAll,
			Bot:          d.BotUsername,
		}
		if g.Status == store.StatusFinished && !g.UpdatedAt.IsZero() {
			resp.FinishedAt = g.UpdatedAt.In(time.FixedZone("MSK", 3*3600)).Format("02.01.2006 15:04 МСК")
		}
		writeJSON(w, resp)
	})

	// API: участие из мини-аппа (кнопка «Участвовать» на посте — startapp).
	// Авторизация по init_data (HMAC); полная проверка подписок; без
	// сообщений от бота — подтверждение рисует страница.
	mux.HandleFunc("/app/api/participate", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"post only"}`, http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			GiveawayID int64  `json:"giveaway_id"`
			InitData   string `json:"init_data"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil || req.GiveawayID <= 0 {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		// init_data — В ТЕЛЕ запроса (и только): страница участвия передаёт
		// его из query страницы. Ранняя версия читала только query/header —
		// все POST'ы участия падали в 401, из-за чего мини-апп-путь участия
		// и был заброшен («не взлетел у клиента»).
		res, ok := d.authorize(r, req.InitData)
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if d.Participate == nil {
			http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		pr := d.Participate(req.GiveawayID, res)
		writeJSON(w, map[string]any{
			"ok":         pr.Ok,
			"error":      pr.Err,
			"notifiable": pr.Notifiable,
			"bot":        d.BotUsername,
		})
	})

	// API: капча — старт (PNG по init_data + giveaway_id).
	mux.HandleFunc("/app/api/captcha/start", func(w http.ResponseWriter, r *http.Request) {
		res, ok := d.authorize(r, "")
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		gid, err := strconv.ParseInt(r.URL.Query().Get("giveaway_id"), 10, 64)
		if err != nil || gid <= 0 {
			http.Error(w, `{"error":"bad giveaway"}`, http.StatusBadRequest)
			return
		}
		g, err := d.DB.GetGiveawayAny(r.Context(), gid)
		if err != nil || g.Status != store.StatusActive {
			http.Error(w, `{"error":"giveaway not active"}`, http.StatusNotFound)
			return
		}
		if d.Captcha == nil {
			http.Error(w, `{"error":"captcha unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		png, err := d.Captcha.GenerateForKey(captchaKey(res, gid))
		if err != nil {
			http.Error(w, `{"error":"captcha gen"}`, http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "image/png")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(png)
	})

	// API: капча — проверка ответа + регистрация участника.
	mux.HandleFunc("/app/api/captcha/verify", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"post only"}`, http.StatusMethodNotAllowed)
			return
		}
		var req struct {
			GiveawayID int64  `json:"giveaway_id"`
			Answer     string `json:"answer"`
			InitData   string `json:"init_data"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil || req.GiveawayID <= 0 {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		res, ok := d.authorize(r, req.InitData)
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		g, err := d.DB.GetGiveawayAny(r.Context(), req.GiveawayID)
		if err != nil || g.Status != store.StatusActive {
			writeJSON(w, map[string]any{"ok": false, "error": "Розыгрыш уже не активен"})
			return
		}
		if d.Captcha == nil || !d.Captcha.VerifyForKey(captchaKey(res, req.GiveawayID), req.Answer) {
			writeJSON(w, map[string]any{"ok": false, "error": "Неверно. Обновите картинку и попробуйте снова"})
			return
		}
		// Капча пройдена — регистрируем участника через полный пайплайн
		// (подписки + идемпотентность, без сообщений от бота).
		var pr ParticipateResult
		if d.Participate != nil {
			pr = d.Participate(req.GiveawayID, res)
		}
		writeJSON(w, map[string]any{
			"ok":         pr.Ok,
			"error":      pr.Err,
			"name":       g.Name,
			"notifiable": pr.Notifiable,
			"bot":        d.BotUsername,
		})
	})

	return mux
}

// authorize проверяет подпись init_data (HMAC) и возвращает user_id.
// init_data берётся из аргумента (тело запроса), затем query, затем header
// X-Init-Data. Пустой BotToken (dev) — авторизация пропускается, user_id=0.
func (d *Deps) authorize(r *http.Request, initData string) (int64, bool) {
	if initData == "" {
		initData = r.URL.Query().Get("init_data")
	}
	if initData == "" {
		initData = r.Header.Get("X-Init-Data")
	}
	if initData == "" {
		return 0, false
	}
	if d.BotToken == "" {
		// Dev-режим без токена: разрешаем (страницы открываются только из
		// мини-аппа, риск локальный).
		return 0, true
	}
	data, err := maxbot.ValidateInitData(initData, d.BotToken)
	if err != nil {
		return 0, false
	}
	return data.User.ID, true
}

// trackTarget — куда вести юзера: конкретный ПОСТ (строго max.ru — защита от
// open redirect), затем публичный канал, фолбэк — главная.
func trackTarget(r *http.Request, d *Deps, link store.UTMLink) string {
	if ref := strings.TrimSpace(link.PostRef); strings.HasPrefix(ref, "https://max.ru/") {
		return ref
	}
	if link.ChannelID != 0 {
		if ch, err := d.DB.GetChannel(r.Context(), link.ChannelID, link.OwnerUserID); err == nil {
			if url := util.ChannelURL(ch.Link); url != "" {
				return url
			}
		}
	}
	return "/app/index.html"
}

// captchaKey — ключ ожидающей капчи в мини-аппе.
func captchaKey(userID, giveawayID int64) string {
	return fmt.Sprintf("%d:%d", userID, giveawayID)
}

func serve(w http.ResponseWriter, name string) {
	data, err := staticFS.ReadFile("app/" + name)
	if err != nil {
		http.NotFound(w, nil)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(data)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}
