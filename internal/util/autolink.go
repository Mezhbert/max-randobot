package util

import (
	"regexp"
	"strconv"
	"strings"
)

// Готовые markdown-ссылки [текст](url) и <url> — защита от двойной обёртки.
var (
	autoLinkMdRe    = regexp.MustCompile(`\[[^\]\x00]*\]\([^\)\s\x00]+\)`)
	autoLinkAngleRe = regexp.MustCompile(`<https?://[^>\s\x00]+>`)
	// «Голый» URL: разрешённые RFC3986 символы; хвостовая пунктуация и
	// markdown-маркеры снимаются после матча (см. AutoLinkMarkdown).
	autoLinkBareRe = regexp.MustCompile(`https?://[A-Za-z0-9\-._~:/?#\[\]@!$&'()*+,;=%]+`)
)

// autoLinkTrim — что снимать с хвоста URL: пунктуация предложения и
// markdown-маркеры (**, __, ~~), случайно захваченные регуляркой.
const autoLinkTrim = ".,;:!?*_~"

// AutoLinkMarkdown оборачивает «голые» URL в markdown-ссылки [url](url).
//
// Зачем: чистый URL в markdown-посте MAX может не стать кликабельным
// (зависит от клиента), а явная [url](url) кликабельна всегда. Уже
// оформленные [текст](url) и <https://…> не трогаются — двойная обёртка
// ломала бы разметку. Хвостовая пунктуация (. , ! ?) и markdown-маркеры
// (**bold**, ~~strike~~) остаются за пределами ссылки.
func AutoLinkMarkdown(s string) string {
	if !strings.Contains(s, "://") {
		return s
	}

	// 1) Маскируем уже оформленные ссылки, чтобы не оборачивать повторно.
	var masked []string
	mask := func(m string) string {
		masked = append(masked, m)
		return "\x00" + strconv.Itoa(len(masked)-1) + "\x00"
	}
	s = autoLinkMdRe.ReplaceAllStringFunc(s, mask)
	s = autoLinkAngleRe.ReplaceAllStringFunc(s, mask)

	// 2) Оборачиваем оставшиеся голые URL; снятый хвост (пунктуация,
	// markdown-маркеры, незакрытая скобка) возвращается после ссылки.
	s = autoLinkBareRe.ReplaceAllStringFunc(s, func(orig string) string {
		m := orig
		for len(m) > 0 && strings.ContainsRune(autoLinkTrim, rune(m[len(m)-1])) {
			m = m[:len(m)-1]
		}
		// Незакрытая «)» в хвосте — часть предложения, не URL.
		for strings.HasSuffix(m, ")") && strings.Count(m, ")") > strings.Count(m, "(") {
			m = m[:len(m)-1]
		}
		if !strings.HasPrefix(m, "http") {
			return orig // весь «URL» оказался пунктуацией — не трогаем
		}
		return "[" + m + "](" + m + ")" + orig[len(m):]
	})

	// 3) Возвращаем замаскированные фрагменты.
	for i, v := range masked {
		s = strings.Replace(s, "\x00"+strconv.Itoa(i)+"\x00", v, 1)
	}
	return s
}
