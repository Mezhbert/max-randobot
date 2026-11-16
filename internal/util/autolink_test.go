package util

import "testing"

func TestAutoLinkMarkdown(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"без ссылок", "просто текст", "просто текст"},
		{"голый URL", "смотри https://max.ru/ch/42",
			"смотри [https://max.ru/ch/42](https://max.ru/ch/42)"},
		{"хвостовая пунктуация снаружи", "жми https://max.ru, быстро!",
			"жми [https://max.ru](https://max.ru), быстро!"},
		{"готовая ссылка не тронута", "уже [пост](https://max.ru/p) тут",
			"уже [пост](https://max.ru/p) тут"},
		{"готовая ссылка + голая", "[a](https://a.b) и https://c.d/e",
			"[a](https://a.b) и [https://c.d/e](https://c.d/e)"},
		{"угловая ссылка не тронута", "пиши <https://max.ru> в чат",
			"пиши <https://max.ru> в чат"},
		{"markdown-маркеры снаружи", "**https://max.ru** в жирном",
			"**[https://max.ru](https://max.ru)** в жирном"},
		{"незакрытая скобка снаружи", "(https://max.ru/x) в скобках",
			"([https://max.ru/x](https://max.ru/x)) в скобках"},
		{"скобки внутри URL", "вики https://w.org/a(b) статья",
			"вики [https://w.org/a(b)](https://w.org/a(b)) статья"},
		{"http тоже", "http://old.ru", "[http://old.ru](http://old.ru)"},
		{"два URL в строке", "a https://a.b и https://c.d",
			"a [https://a.b](https://a.b) и [https://c.d](https://c.d)"},
		{"URL с query", "https://max.ru/x?y=1&z=2 конец",
			"[https://max.ru/x?y=1&z=2](https://max.ru/x?y=1&z=2) конец"},
		{"t.me стиль", "пишите https://max.ru/@user.",
			"пишите [https://max.ru/@user](https://max.ru/@user)."},
	}
	for _, c := range cases {
		if got := AutoLinkMarkdown(c.in); got != c.want {
			t.Errorf("%s:\n  in:  %q\n  got: %q\n  want: %q", c.name, c.in, got, c.want)
		}
	}
}
