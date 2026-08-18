// Package webapp — мини-апп бота: статика (HTML) + JSON API.
// Раздаётся тем же HTTP-сервером что и webhook (через cloudflare tunnel),
// URL прописывается в MasterBot → кнопка open_app ведёт сюда.
package webapp

import (
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
	"github.com/maxbot/giveawaybot/internal/captcha"
	"github.com/maxbot/giveawaybot/internal/store"
)

//go:embed app/*
var staticFS embed.FS

// Deps — зависимости мини-аппа.
type Deps struct {
	DB        *store.Store
	Captcha   *captcha.Service
	BotToken  string // для ValidateInitData (пусто — валидация отключена)
	// RegisterParticipant вызывается после успешной капчи в мини-аппе:
	// добавляет участника в БД (owner-логика в handlers, тут только колбэк).
	RegisterParticipant func(giveawayID, userID int64)
}

// Handler возвращает HTTP-хендлеры мини-аппа:
//   GET /app/                  → редирект на results.html
//   GET /app/results.html      → страница результатов
//   GET /app/captcha.html      → страница капчи
//   GET /app/api/giveaway/{id} → JSON с данными розыгрыша
//   GET /app/api/captcha/start → PNG-капча (по init_data+giveaway_id)
//   POST /app/api/captcha/verify → проверка ответа + регистрация участника
func Handler(d Deps, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// Статика.
	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/app/" || path == "/app" {
			// Корень мини-аппа — главная с описанием бота (при открытии
			// кнопкой без start_param именно она и нужна).
			http.Redirect(w, r, "/app/index.html", http.StatusFound)
			return
		}
		// Берём файл из embed (app/...), защищаясь от path traversal.
		name := path[len("/app/"):]
		if name == "" || name == "results" {
			name = "results.html"
		}
		data, err := staticFS.ReadFile("app/" + name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})

	// API: данные розыгрыша.
	mux.HandleFunc("/app/api/giveaway/", func(w http.ResponseWriter, r *http.Request) {
		idStr := r.URL.Path[len("/app/api/giveaway/"):]
		gid, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil || gid <= 0 {
			http.Error(w, `{"error":"bad id"}`, http.StatusBadRequest)
			return
		}

		// initData передаётся query-параметром (мини-апп авторизует юзера).
		// Публичные данные розыгрыша (итоги) доступны без авторизации.
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
				name = u.FirstName
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
		}{
			Name:         g.Name,
			Status:       g.Status,
			Participants: participants,
			WinnersCount: g.WinnersCount,
			Winners:      winners,
		}
		if g.Status == "finished" && !g.UpdatedAt.IsZero() {
			resp.FinishedAt = g.UpdatedAt.In(time.FixedZone("MSK", 3*3600)).Format("02.01.2006 15:04 МСК")
		}

		writeJSON(w, resp)
	})

	// API: капча — старт (PNG по init_data + giveaway_id).
	mux.HandleFunc("/app/api/captcha/start", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := validateInitDataUser(d, r)
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
		png, err := d.Captcha.GenerateForKey(captchaKey(userID, gid))
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
		userID, ok := validateInitDataUser(d, r)
		if !ok {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var req struct {
			GiveawayID int64  `json:"giveaway_id"`
			Answer     string `json:"answer"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil || req.GiveawayID <= 0 {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		g, err := d.DB.GetGiveawayAny(r.Context(), req.GiveawayID)
		if err != nil || g.Status != store.StatusActive {
			writeJSON(w, map[string]any{"ok": false, "error": "Розыгрыш уже не активен"})
			return
		}
		if !d.Captcha.VerifyForKey(captchaKey(userID, req.GiveawayID), req.Answer) {
			writeJSON(w, map[string]any{"ok": false, "error": "Неверно. Обновите картинку и попробуйте снова"})
			return
		}
		// Капча пройдена — регистрируем участника (колбэк в handlers).
		if d.RegisterParticipant != nil {
			d.RegisterParticipant(req.GiveawayID, userID)
		}
		writeJSON(w, map[string]any{"ok": true, "name": g.Name})
	})

	return mux
}

// validateInitDataUser проверяет подпись init_data (HMAC) и возвращает user_id.
// Если BotToken не задан (dev) — авторизация пропускается и возвращается 0+true
// только при наличии непустого init_data (soft-mode для локальной отладки).
func validateInitDataUser(d Deps, r *http.Request) (int64, bool) {
	initData := r.URL.Query().Get("init_data")
	if initData == "" {
		// POST может нести init_data в теле вместе с ответом — fallback на header.
		initData = r.Header.Get("X-Init-Data")
	}
	if initData == "" {
		return 0, false
	}
	if d.BotToken == "" {
		// Dev-режим без токена: разрешаем (кнопка капчи работает только из
		// открытого мини-аппа, риск локальный).
		return 0, true
	}
	data, err := maxbot.ValidateInitData(initData, d.BotToken)
	if err != nil {
		return 0, false
	}
	return data.User.ID, true
}

// captchaKey — ключ ожидающей капчи в мини-аппе.
func captchaKey(userID, giveawayID int64) string {
	return fmt.Sprintf("%d:%d", userID, giveawayID)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Access-Control-Allow-Origin", "*")
	_ = json.NewEncoder(w).Encode(v)
}
