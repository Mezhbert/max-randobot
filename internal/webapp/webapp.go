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
	"context"
	"embed"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"sort"
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

	// SendPost — отправить готовый пост розыгрыша админу в чат (превью
	// точным видом: текст + медиа + кнопка). Реализация в handlers.
	SendPost func(ctx context.Context, userID int64, g store.Giveaway) error

	// Создание из миниаппа (реализации в handlers).
	CreateGiveaway   func(ctx context.Context, uid int64, in CreateGiveawayReq) (int64, string, error)
	CreateTracking   func(ctx context.Context, uid int64, in CreateTrackingReq) ([]string, string, error)
	AddChannel       func(ctx context.Context, uid int64, link string) (string, string, error)
	UploadMedia      func(ctx context.Context, data []byte, filename, kind string) (string, string, error)

	// CheckSubscriber — живая проверка «подписан ли юзер на канал»
	// (maxapi.IsSubscriber). Первичная проверка в момент клика по
	// трекинг-ссылке: уже-участники фиксируются сразу (user_added для них не
	// придёт). nil — проверка отключена.
	CheckSubscriber func(ctx context.Context, chatID, userID int64) (bool, error)
}

// ---------- очередь первичных проверок (клик → уже-участник?) ----------
//
// Клик по трекинг-ссылке не должен ждать проверки подписки (юзер ждёт
// редирект) — проверка уходит в очередь и выполняется фоновыми воркерами.
// Подписки/отписки дальше трекаются событиями user_added/user_removed.

type clickCheckTask struct {
	linkID, userID int64
}

var (
	clickChecks       = make(chan clickCheckTask, 256)
	clickWorkersOnce  bool
	clickWorkersDeps  *Deps
	clickWorkersLog   *slog.Logger
)

// enqueueClickCheck — поставить первичную проверку в очередь (неблокирующе;
// переполнение = пропускаем: событие user_added всё равно закроет кейс).
func enqueueClickCheck(linkID, userID int64) {
	select {
	case clickChecks <- clickCheckTask{linkID: linkID, userID: userID}:
	default:
	}
}

// runClickWorkers — фоновые воркеры очереди (стартуют один раз на процесс).
func runClickWorkers(d *Deps, log *slog.Logger) {
	if clickWorkersOnce || d.CheckSubscriber == nil {
		return
	}
	clickWorkersOnce = true
	clickWorkersDeps, clickWorkersLog = d, log
	for i := 0; i < 2; i++ {
		go func() {
			for t := range clickChecks {
				clickCheck(t)
			}
		}()
	}
}

// clickCheck — одна задача очереди: ссылка → канал → IsSubscriber → запись.
func clickCheck(t clickCheckTask) {
	d, log := clickWorkersDeps, clickWorkersLog
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	link, err := d.DB.GetUTMLinkByID(ctx, t.linkID)
	if err != nil || link.ChannelID == 0 {
		return
	}
	ch, err := d.DB.GetChannel(ctx, link.ChannelID, link.OwnerUserID)
	if err != nil {
		return
	}
	sub, err := d.CheckSubscriber(ctx, ch.ChatID, t.userID)
	if err != nil {
		if log != nil {
			log.Debug("click check", "err", err, "user", t.userID)
		}
		return
	}
	if sub {
		// Уже участник: фиксируем сразу, отдельным флагом
		// («уже были участниками на момент клика» — своя метрика).
		if err := d.DB.UpsertUTMSubscriberAtClick(ctx, t.linkID, t.userID, ch.ChatID); err != nil && log != nil {
			log.Debug("click check save", "err", err)
		}
	}
}

// CaptchaService — минимальный интерфейс капчи (совместим с internal/captcha).
// Ключ капчи мини-аппа — "<userID>:<giveawayID>" (не пересекается с чатовой
// капчей, ключ которой — просто userID).
type CaptchaService interface {
	GenerateForKey(key string) ([]byte, error)
	VerifyForKey(key, answer string) bool
}

// CreateGiveawayReq — запрос создания розыгрыша из миниаппа.
type CreateGiveawayReq struct {
	Name           string             `json:"name"`
	PostText       string             `json:"post_text"`
	PostFormat     string             `json:"post_format"`
	MediaTokens    []store.MediaToken `json:"media_tokens"`
	ButtonText     string             `json:"button_text"`
	PubChannels    []int64            `json:"pub_channels"`
	SubChannels    []int64            `json:"sub_channels"`
	PublishNow     bool               `json:"publish_now"`
	PublishAt      string             `json:"publish_at"`
	FinishMode     string             `json:"finish_mode"`
	FinishAt       string             `json:"finish_at"`
	FinishCount    int                `json:"finish_count"`
	Winners        int                `json:"winners"`
	WinnersMessage string             `json:"winners_message"`
	Referral       bool               `json:"referral"`
	Captcha        bool               `json:"captcha"`
	ResultsInPost  bool               `json:"results_in_post"`
	ResultsText    string             `json:"results_text"`
	SaveDraft      bool               `json:"save_draft"`
}

// CreateTrackingReq — запрос трекинг-ссылки из миниаппа.
type CreateTrackingReq struct {
	Mode       string   `json:"mode"`
	ChannelID  int64    `json:"channel_id"`
	ChannelIDs []int64  `json:"channel_ids"`
	Name       string   `json:"name"`
	Cost       float64  `json:"cost"`
	LinkType   string   `json:"link_type"`
	PostRef    string   `json:"post_ref"`
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
	runClickWorkers(d, log)
	mux := http.NewServeMux()

	// Статика мини-аппа.
	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/app/" || path == "/app" || path == "/app/index.html" {
			// Серверная маршрутизация start_param (если клиент передал его
			// query-параметром — веб-версия MAX так делает): сразу на нужную
			// страницу, без клиентского JS-редиректа (в мобильном клиенте он
			// может не выполниться). Дублирует роутер index.html — надёжнее.
			if sp := firstNonEmpty(r.URL.Query().Get("startapp"), r.URL.Query().Get("start_param")); sp != "" {
				if dest, ok := miniAppRoute(sp); ok {
					q := "start_param=" + url.QueryEscape(sp) +
						"&init_data=" + url.QueryEscape(r.URL.Query().Get("init_data"))
					log.Info("miniapp route (server)", "start_param", sp, "dest", dest)
					http.Redirect(w, r, dest+"?"+q, http.StatusFound)
					return
				}
			}
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
			// Первичная проверка «уже участник?» — в очередь (редирект не
			// ждёт); подписка/отписка дальше — событиями вебхука.
			enqueueClickCheck(link.ID, userID)
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

	// ---------- API действий из миниаппа (хаб, rev Great-Gifts-стиль) ----------

	// Создание розыгрыша (полный функционал) из миниаппа.
	mux.HandleFunc("/app/api/giveaways/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"post only"}`, http.StatusMethodNotAllowed)
			return
		}
		userID, ok := d.authorize(r, r.URL.Query().Get("init_data"))
		if !ok || userID == 0 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var req CreateGiveawayReq
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		if d.CreateGiveaway == nil {
			http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		gid, vmsg, err := d.CreateGiveaway(r.Context(), userID, req)
		if err != nil {
			log.Warn("app create giveaway", "err", err, "user", userID)
			writeJSON(w, map[string]any{"ok": false, "error": "Внутренняя ошибка, попробуйте позже"})
			return
		}
		writeJSON(w, map[string]any{"ok": vmsg == "", "error": vmsg, "giveaway_id": gid})
	})

	// Создание трекинг-ссылки (ads unique/super, contest пачкой).
	mux.HandleFunc("/app/api/tracking/create", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"post only"}`, http.StatusMethodNotAllowed)
			return
		}
		userID, ok := d.authorize(r, r.URL.Query().Get("init_data"))
		if !ok || userID == 0 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var req CreateTrackingReq
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		if d.CreateTracking == nil {
			http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		codes, vmsg, err := d.CreateTracking(r.Context(), userID, req)
		if err != nil {
			log.Warn("app create tracking", "err", err, "user", userID)
			writeJSON(w, map[string]any{"ok": false, "error": "Внутренняя ошибка, попробуйте позже"})
			return
		}
		writeJSON(w, map[string]any{"ok": vmsg == "", "error": vmsg, "codes": codes,
			"bot": d.BotUsername})
	})

	// Добавление канала по ссылке/@username.
	mux.HandleFunc("/app/api/channels/add", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"post only"}`, http.StatusMethodNotAllowed)
			return
		}
		userID, ok := d.authorize(r, r.URL.Query().Get("init_data"))
		if !ok || userID == 0 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var req struct {
			Link string `json:"link"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<12)).Decode(&req); err != nil || req.Link == "" {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		if d.AddChannel == nil {
			http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		title, vmsg, err := d.AddChannel(r.Context(), userID, req.Link)
		if err != nil {
			log.Warn("app add channel", "err", err, "user", userID)
			writeJSON(w, map[string]any{"ok": false, "error": "Внутренняя ошибка, попробуйте позже"})
			return
		}
		writeJSON(w, map[string]any{"ok": vmsg == "", "error": vmsg, "title": title})
	})

	// Загрузка медиа (multipart: file=..., kind=image|video) → токен.
	mux.HandleFunc("/app/api/upload", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"post only"}`, http.StatusMethodNotAllowed)
			return
		}
		userID, ok := d.authorize(r, r.URL.Query().Get("init_data"))
		if !ok || userID == 0 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		if d.UploadMedia == nil {
			http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		if err := r.ParseMultipartForm(32 << 20); err != nil {
			http.Error(w, `{"error":"bad form"}`, http.StatusBadRequest)
			return
		}
		file, hdr, err := r.FormFile("file")
		if err != nil {
			http.Error(w, `{"error":"no file"}`, http.StatusBadRequest)
			return
		}
		defer file.Close()
		kind := r.FormValue("kind")
		if kind != "image" && kind != "video" {
			kind = "image"
		}
		data, err := io.ReadAll(io.LimitReader(file, 50<<20))
		if err != nil {
			http.Error(w, `{"error":"read"}`, http.StatusBadRequest)
			return
		}
		token, attType, err := d.UploadMedia(r.Context(), data, hdr.Filename, kind)
		if err != nil {
			log.Warn("app upload", "err", err, "user", userID)
			writeJSON(w, map[string]any{"ok": false, "error": "Не удалось загрузить файл (до 50 МБ, JPG/PNG/MP4)"})
			return
		}
		writeJSON(w, map[string]any{"ok": true, "token": token, "type": attType})
	})

	// ---------- API аналитики для мини-аппа (rev 13.11) ----------

	// Каналы владельца со сводной статистикой ссылок.
	mux.HandleFunc("/app/api/analytics/channels", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := d.authorize(r, r.URL.Query().Get("init_data"))
		if !ok || userID == 0 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		channels, err := d.DB.ListChannels(r.Context(), userID)
		if err != nil {
			writeJSON(w, map[string]any{"error": "db"})
			return
		}
		type chOut struct {
			ID       int64  `json:"id"`
			Title    string `json:"title"`
			IsPublic bool   `json:"is_public"`
			Admin    bool   `json:"admin"`
			Links    int    `json:"links"`
			Clicks   int64  `json:"clicks"`
			Signups  int64  `json:"signups"`
			AddedAt  string `json:"added_at"`
		}
		out := make([]chOut, 0, len(channels))
		for _, c := range channels {
			stats, _ := d.DB.ListUTMStats(r.Context(), userID, c.ID)
			var clicks, signups int64
			for _, st := range stats {
				clicks += st.Clicks
				signups += st.Signups
			}
			title := c.Title
			if title == "" {
				title = fmt.Sprintf("канал #%d", c.ChatID)
			}
			out = append(out, chOut{
				ID: c.ID, Title: title, IsPublic: c.IsPublic, Admin: c.IsBotAdmin,
				Links: len(stats), Clicks: clicks, Signups: signups,
				AddedAt: c.CreatedAt.In(msk()).Format("02.01.2006"),
			})
		}
		writeJSON(w, map[string]any{"channels": out})
	})

	// Ссылки канала (сорт name|date) + динамика 30 дней.
	mux.HandleFunc("/app/api/analytics/channel/", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := d.authorize(r, r.URL.Query().Get("init_data"))
		if !ok || userID == 0 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		chID, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/app/api/analytics/channel/"), 10, 64)
		if err != nil {
			http.Error(w, `{"error":"bad id"}`, http.StatusBadRequest)
			return
		}
		if _, err := d.DB.GetChannel(r.Context(), chID, userID); err != nil {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		stats, err := d.DB.ListUTMStats(r.Context(), userID, chID)
		if err != nil {
			writeJSON(w, map[string]any{"error": "db"})
			return
		}
		// Сортировка: date DESC (SQL) — по умолчанию; name — по алфавиту.
		if r.URL.Query().Get("sort") == "name" {
			sort.Slice(stats, func(i, j int) bool { return stats[i].Source < stats[j].Source })
		}
		type linkOut struct {
			ID      int64   `json:"id"`
			Name    string  `json:"name"`
			Type    string  `json:"type"`
			PostRef string  `json:"post_ref"`
			Cost    float64 `json:"cost"`
			Clicks  int64   `json:"clicks"`
			Signups int64   `json:"signups"`
			StillIn int     `json:"still_in"`
			Ret1d   int     `json:"ret_1d"`
			Ret3d   int     `json:"ret_3d"`
			Ret7d   int     `json:"ret_7d"`
			Ret14d  int     `json:"ret_14d"`
			Ret30d  int     `json:"ret_30d"`
			Conv    float64 `json:"conv"`
		}
		links := make([]linkOut, 0, len(stats))
		for _, st := range stats {
			ret, err := d.DB.GetUTMLinkRetention(r.Context(), st.ID)
			if err != nil {
				continue
			}
			conv := 0.0
			if st.Clicks > 0 {
				conv = float64(st.Signups) / float64(st.Clicks) * 100
			}
			links = append(links, linkOut{
				ID: st.ID, Name: st.Source, Type: st.LinkType, PostRef: st.PostRef, Cost: st.Cost,
				Clicks: st.Clicks, Signups: st.Signups, StillIn: ret.StillIn,
				Ret1d: ret.Retained1d, Ret3d: ret.Retained3d, Ret7d: ret.Retained7d,
				Ret14d: ret.Retained14d, Ret30d: ret.Retained30d, Conv: conv,
			})
		}
		dyn, _ := d.DB.ChannelDynamics(r.Context(), chID, 30)
		writeJSON(w, map[string]any{"links": links, "dynamics": dyn})
	})

	// Розыгрыши/посты (включая черновики) для миниаппа: фильтр по статусу,
	// сортировка имя/дата (SQL и так отдаёт новые первыми по updated_at).
	mux.HandleFunc("/app/api/analytics/giveaways", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := d.authorize(r, r.URL.Query().Get("init_data"))
		if !ok || userID == 0 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		items, err := d.DB.ListGiveaways(r.Context(), userID, "")
		if err != nil {
			writeJSON(w, map[string]any{"error": "db"})
			return
		}
		type gwOut struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Status  string `json:"status"`
			When    string `json:"when"`
			Parts   int    `json:"parts"`
			Winners int    `json:"winners"`
		}
		out := make([]gwOut, 0, len(items))
		for _, g := range items {
			parts, _ := d.DB.CountParticipants(r.Context(), g.ID)
			when := g.UpdatedAt
			switch g.Status {
			case store.StatusActive:
				if g.FinishAt != nil {
					when = *g.FinishAt
				}
			case store.StatusScheduled:
				if g.PublishAt != nil {
					when = *g.PublishAt
				}
			}
			name := g.Name
			if name == "" {
				name = "без названия"
			}
			out = append(out, gwOut{
				ID: g.ID, Name: name, Status: g.Status,
				When: when.In(msk()).Format("02.01.2006 15:04"),
				Parts: parts, Winners: g.WinnersCount,
			})
		}
		writeJSON(w, map[string]any{"giveaways": out})
	})

	// Отправить готовый пост розыгрыша админу в чат (кнопка из миниаппа).
	// Реализация переиспользует превью мастера: пост точным видом + медиа.
	mux.HandleFunc("/app/api/analytics/send-post", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, `{"error":"post only"}`, http.StatusMethodNotAllowed)
			return
		}
		userID, ok := d.authorize(r, r.URL.Query().Get("init_data"))
		if !ok || userID == 0 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		var req struct {
			GiveawayID int64 `json:"giveaway_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1<<16)).Decode(&req); err != nil || req.GiveawayID <= 0 {
			http.Error(w, `{"error":"bad request"}`, http.StatusBadRequest)
			return
		}
		g, err := d.DB.GetGiveaway(r.Context(), req.GiveawayID, userID)
		if err != nil {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		if d.SendPost == nil {
			http.Error(w, `{"error":"unavailable"}`, http.StatusServiceUnavailable)
			return
		}
		if err := d.SendPost(r.Context(), userID, g); err != nil {
			writeJSON(w, map[string]any{"ok": false, "error": "send failed"})
			return
		}
		writeJSON(w, map[string]any{"ok": true})
	})

	// Детальная статистика одной ссылки + подписчики + кликнувшие.
	mux.HandleFunc("/app/api/analytics/link/", func(w http.ResponseWriter, r *http.Request) {
		userID, ok := d.authorize(r, r.URL.Query().Get("init_data"))
		if !ok || userID == 0 {
			http.Error(w, `{"error":"unauthorized"}`, http.StatusUnauthorized)
			return
		}
		linkID, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/app/api/analytics/link/"), 10, 64)
		if err != nil {
			http.Error(w, `{"error":"bad id"}`, http.StatusBadRequest)
			return
		}
		link, err := d.DB.GetUTMLinkByID(r.Context(), linkID)
		if err != nil || link.OwnerUserID != userID {
			http.Error(w, `{"error":"not found"}`, http.StatusNotFound)
			return
		}
		ret, _ := d.DB.GetUTMLinkRetention(r.Context(), linkID)
		subs, _ := d.DB.ListUTMSubscribers(r.Context(), linkID)
		clickers, _ := d.DB.ListUTMClickers(r.Context(), linkID)
		conv := 0.0
		if link.Clicks > 0 {
			conv = float64(link.Signups) / float64(link.Clicks) * 100
		}
		writeJSON(w, map[string]any{
			"link": link, "retention": ret, "subscribers": subs, "clickers": clickers,
			"conv": conv,
		})
	})

	// Лог запросов мини-аппа (диагностика QA: видно, что реально приходит от
	// клиента; init_data не логируем — только факт наличия).
	return logRequests(mux, log)
}

// logRequests — логирование запросов мини-аппа (HTML и API) для диагностики.
func logRequests(next http.Handler, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasPrefix(r.URL.Path, "/app/api/") {
			log.Info("app api", "path", r.URL.Path, "init_data", r.URL.Query().Get("init_data") != "")
		} else {
			log.Info("app page", "path", r.URL.Path, "query", sanitizeQuery(r.URL.Query()))
		}
		next.ServeHTTP(w, r)
	})
}

// sanitizeQuery — query без чувствительных значений (init_data подписана
// юзером — в лог только факт наличия).
func sanitizeQuery(q url.Values) string {
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return strings.Join(keys, ",")
}

// miniAppRoute — страница мини-аппа для start_param.
func miniAppRoute(sp string) (string, bool) {
	switch {
	case strings.HasPrefix(sp, "part_"), strings.HasPrefix(sp, "g_"):
		return "/app/participate.html", true
	case strings.HasPrefix(sp, "results_"), strings.HasPrefix(sp, "r_"):
		return "/app/results.html", true
	case strings.HasPrefix(sp, "analytics"):
		return "/app/analytics.html", true
	}
	return "", false
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if v != "" {
			return v
		}
	}
	return ""
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

// msk — зона МСК для форматирования дат в API миниаппа.
func msk() *time.Location {
	if loc, err := time.LoadLocation("Europe/Moscow"); err == nil {
		return loc
	}
	return time.FixedZone("MSK", 3*3600)
}
