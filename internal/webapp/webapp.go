// Package webapp — мини-апп бота: статика (HTML) тем же HTTP-сервером, что и
// webhook (через cloudflare tunnel). Мини-апп — ТОЛЬКО главный экран с
// описанием бота (+ страница справки для кнопки «Поддержка»): участие и итоги
// живут в чате бота и постах каналов, отдельные страницы и API удалены.
package webapp

import (
	"embed"
	"net/http"
)

//go:embed app/*
var staticFS embed.FS

// Handler возвращает HTTP-хендлер мини-аппа:
//
//	GET /app/  → редирект на главный экран (index.html — описание бота)
//	GET /app/* → статика из embed (index.html, help.html)
func Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/app/", func(w http.ResponseWriter, r *http.Request) {
		path := r.URL.Path
		if path == "/app/" || path == "/app" {
			// Любое открытие мини-аппа (в т.ч. со start_param от старых
			// open_app-кнопок) — главный экран с описанием.
			http.Redirect(w, r, "/app/index.html", http.StatusFound)
			return
		}
		// Берём файл из embed (app/...), защищаясь от path traversal
		// (embed.FS отвергает недопустимые пути — «..» даёт 404).
		name := path[len("/app/"):]
		data, err := staticFS.ReadFile("app/" + name)
		if err != nil {
			// Удалённые страницы (participate/results/captcha из старых
			// open_app-кнопок) — на главный экран, а не 404.
			http.Redirect(w, r, "/app/index.html", http.StatusFound)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(data)
	})
	return mux
}
