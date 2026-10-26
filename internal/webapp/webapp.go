// Package webapp — мини-апп бота.
//
// Два назначения:
//  1. ГЛАВНОЕ: бесшовный трекинг-редиректор. Ссылка вида
//     https://max.ru/<bot>?startapp=u_<code> открывает ЭТУ страницу как
//     мини-апп БЕЗ запуска бота (официальная механика MAX, см. dev.max.ru
//     /docs/webapps/introduction): клиент передаёт подписанный user
//     (WebAppData), мы валидируем HMAC → фиксируем клик С user_id →
//     отвечаем, куда редиректить (канал/пост) → JS мгновенно ведёт юзера
//     туда. Юзер видит только короткий спиннер — чат бота не открывается.
//  2. Главный экран-описание (если мини-апп открыт без start_param).
package webapp

import (
	"embed"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"strings"

	maxbot "github.com/max-messenger/max-bot-api-client-go/v2"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
)

//go:embed app/*
var staticFS embed.FS

// Deps — зависимости мини-аппа.
type Deps struct {
	DB       *store.Store
	Captcha  CaptchaService // nil — капча отключена (пока не используется)
	BotToken string         // для валидации init_data
}

// CaptchaService — минимальный интерфейс капчи (совместим с internal/captcha).
type CaptchaService interface {
	GenerateForKey(key string) ([]byte, error)
	VerifyForKey(key, answer string) bool
}

// Handler возвращает HTTP-хендлеры мини-аппа:
//
//	GET  /app/  → трекинг-редиректор (start_param=u_<code>) или главная
//	POST /app/api/track → клик: init_data → user_id → редирект-цель
func Handler(d *Deps, log *slog.Logger) http.Handler {
	mux := http.NewServeMux()

	// Статика мини-аппа (редиректор + главная).
	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/app/" || path == "/app" || path == "/app/index.html" {
			serve(w, "index.html")
			return
		}
		// Остальная статика из embed (help.html и т.п.); отсутствующие
		// страницы — на главную-редиректор.
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

	return mux
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
