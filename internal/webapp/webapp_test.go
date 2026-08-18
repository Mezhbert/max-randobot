package webapp

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestStartParamFromInitData — все варианты кодирования init_data, которые
// шлют клиенты MAX (проверено на живом вэбаппе): голая query-string,
// %-encoded (двойное кодирование).
func TestStartParamFromInitData(t *testing.T) {
	cases := []struct {
		name     string
		initData string
		want     string
	}{
		{"голая qs", "user=%7B%22id%22%3A1%7D&auth_date=123&start_param=part_5&hash=abc", "part_5"},
		{"двойное кодирование", url.QueryEscape("user=%7B%22id%22%3A1%7D&auth_date=123&start_param=results_7&hash=abc"), "results_7"},
		{"captcha", "start_param=captcha_9&hash=x", "captcha_9"},
		{"нет параметра", "user=%7B%22id%22%3A1%7D&hash=abc", ""},
		{"пусто", "", ""},
		{"мусор", "%%%", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			assert.Equal(t, c.want, startParamFromInitData(c.initData))
		})
	}
}

// TestMiniAppRoute — маппинг payload → страница.
func TestMiniAppRoute(t *testing.T) {
	dest, ok := miniAppRoute("part_1")
	assert.True(t, ok)
	assert.Equal(t, "/app/participate.html", dest)

	dest, ok = miniAppRoute("results_2")
	assert.True(t, ok)
	assert.Equal(t, "/app/results.html", dest)

	dest, ok = miniAppRoute("captcha_3")
	assert.True(t, ok)
	assert.Equal(t, "/app/captcha.html", dest)

	_, ok = miniAppRoute("unknown_x")
	assert.False(t, ok)
}
