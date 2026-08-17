// Package webapp — мини-апп бота: статика (HTML) + JSON API.
// Раздаётся тем же HTTP-сервером что и webhook (через cloudflare tunnel),
// URL прописывается в MasterBot → кнопка open_app ведёт сюда.
package webapp

import (
	"embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strconv"
	"time"

	"github.com/maxbot/giveawaybot/internal/store"
)

//go:embed app/*
var staticFS embed.FS

// Handler возвращает HTTP-хендлеры мини-аппа:
//   GET /app/                  → редирект на results.html
//   GET /app/results.html      → страница результатов
//   GET /app/api/giveaway/{id} → JSON с данными розыгрыша
func Handler(db *store.Store, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// Статика.
	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/app/" || path == "/app" {
			http.Redirect(w, r, "/app/results.html", http.StatusFound)
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
		// Публичные данные розыгрыша (итоги) доступны без авторизации;
		// расширенная аналитика (владельцу) — todo при появлении валидации.
		initData := r.URL.Query().Get("init_data")
		_ = initData // Валидация подписи initData — следующий шаг (нужен бот-токен).

		g, err := db.GetGiveawayAny(r.Context(), gid)
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
			if u, err := db.GetUser(r.Context(), wid); err == nil && u.FirstName != "" {
				name = u.FirstName
			}
			winners = append(winners, winner{Name: name})
		}

		participants, _ := db.CountParticipants(r.Context(), g.ID)

		resp := struct {
			Name        string   `json:"name"`
			Status      string   `json:"status"`
			Participants int      `json:"participants"`
			WinnersCount int      `json:"winners_count"`
			Winners     []winner `json:"winners"`
			FinishedAt  string   `json:"finished_at,omitempty"`
		}{
			Name:        g.Name,
			Status:      g.Status,
			Participants: participants,
			WinnersCount: g.WinnersCount,
			Winners:     winners,
		}
		if g.Status == "finished" && !g.UpdatedAt.IsZero() {
			resp.FinishedAt = g.UpdatedAt.In(time.FixedZone("MSK", 3*3600)).Format("02.01.2006 15:04 МСК")
		}

		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		w.Header().Set("Access-Control-Allow-Origin", "*")
		_ = json.NewEncoder(w).Encode(resp)
	})

	return mux
}
