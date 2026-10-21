// redirect.go — бесшовные трекинг-ссылки: GET /r/<code>.
//
// Как это работает (и почему без прослоек для юзера):
//   1. Владелец размещает ссылку https://<наш-домен>/r/<code> в посте/рекламе.
//   2. Юзер кликает → наш сервер мгновенно засчитывает клик
//      (store.RecordAnonymousClick) и отвечает 302 на канал/пост.
//   3. Юзер видит канал/пост — промежуточной страницы нет, редирект незаметен.
//
// Ограничение честно задокументировано: HTTP-клик анонимен (user_id неизвестен),
// поэтому атрибуция «кто именно подписался» для /r/-ссылок недоступна — для
// точечной атрибуции остаётся диплинк-вариант (utm_<code> через бот).
package webapp

import (
	"context"
	"log/slog"
	"net/http"
	"strings"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
)

// RedirectHandler — GET /r/<code>: клик + 302 на цель.
func RedirectHandler(db *store.Store, log *slog.Logger) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := context.Background()
		code := strings.TrimPrefix(r.URL.Path, "/r/")
		code = strings.Trim(code, "/ ")
		if code == "" || len(code) > 64 {
			http.Redirect(w, r, "/app/index.html", http.StatusFound)
			return
		}

		link, err := db.GetUTMLinkByCode(ctx, code)
		if err != nil {
			// Битый/устаревший код — на главный экран, не 404.
			http.Redirect(w, r, "/app/index.html", http.StatusFound)
			return
		}

		// Клик считаем всегда; ошибка БД не должна ломать переход.
		if err := db.RecordAnonymousClick(ctx, link.ID); err != nil {
			log.Warn("anonymous click", "err", err, "code", code)
		}

		http.Redirect(w, r, redirectTarget(ctx, db, link), http.StatusFound)
	})
}

// redirectTarget — куда вести: пост-источник (строго https://max.ru/... —
// защита от open redirect на произвольный домен), затем публичный канал,
// фолбэк — главный экран мини-аппа.
func redirectTarget(ctx context.Context, db *store.Store, link store.UTMLink) string {
	if ref := strings.TrimSpace(link.PostRef); strings.HasPrefix(ref, "https://max.ru/") {
		return ref
	}
	if link.ChannelID != 0 && db != nil {
		if ch, err := db.GetChannel(ctx, link.ChannelID, link.OwnerUserID); err == nil {
			if url := util.ChannelURL(ch.Link); url != "" {
				return url
			}
		}
	}
	return "/app/index.html"
}
