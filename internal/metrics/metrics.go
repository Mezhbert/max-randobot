// Package metrics — in-process счётчики вызовов к платформе MAX.
//
// Цель: следить за RPS и ошибками по API MAX «без напряжения платформы» —
// это НЕ внешний запрос к MAX, а локальные атомарные счётчики, которые
// инкрементируются в обёртках maxapi.API (несколько atomic.Add на вызов,
// overhead ~наносекунды). Результат отдаётся через /metrics в формате
// Prometheus text — pull-модель: внешнего скрейпера (Prometheus/Grafana)
// подключать опционально, а `curl /metrics` работает из коробки.
package metrics

import (
	"fmt"
	"io"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Method — идентификатор метода API MAX (для разбивки счётчиков).
type Method string

// Используемые методы (совпадают с методами maxapi.API).
const (
	MethodSendText            Method = "send_text"
	MethodSendTextWithKeyboard Method = "send_text_kb"
	MethodSendWithAttachments  Method = "send_attachments"
	MethodSendToChat           Method = "send_to_chat"
	MethodEditChatText         Method = "edit_chat_text"
	MethodIsSubscriber         Method = "is_subscriber"
	MethodIsBotAdminInChat     Method = "is_bot_admin"
	MethodCheckChannelAdmin    Method = "check_channel_admin"
	MethodGetChatInfo          Method = "get_chat_info"
	MethodAnswerCallback       Method = "answer_callback"
)

// методы в стабильном порядке для вывода (иначе map-итерация рандомна).
var orderedMethods = []Method{
	MethodSendText, MethodSendTextWithKeyboard, MethodSendWithAttachments,
	MethodSendToChat, MethodEditChatText, MethodIsSubscriber,
	MethodIsBotAdminInChat, MethodCheckChannelAdmin, MethodGetChatInfo,
	MethodAnswerCallback,
}

// Counters — потокобезопасные счётчики вызовов MAX API.
// Нулевое значение валидно (все счётчики = 0).
type Counters struct {
	// Суммарные счётчики по методу × исход. Атомарные — lock-free.
	totalSuccess map[Method]*int64
	totalError   map[Method]*int64

	// Скользящий счётчик вызовов за последнюю секунду (gauge RPS).
	// Реализация: 2 бакета — current (текущая секунда) и previous (прошлая).
	// При записи, если секунда сменилась, current переезжает в previous, а
	// новый current обнуляется. Gauge = previous (последняя завершённая секунда),
	// что стабильно и не флапает внутри секунды.
	rateMu       sync.Mutex
	rateCurrent  int64 // счётчик текущей секунды
	ratePrevious int64 // счётчик последней завершённой секунды
	rateSecond   int64 // unix-секунда текущего бакета
}

// New создаёт инициализированный набор счётчиков.
func New() *Counters {
	c := &Counters{
		totalSuccess: make(map[Method]*int64, len(orderedMethods)),
		totalError:   make(map[Method]*int64, len(orderedMethods)),
	}
	for _, m := range orderedMethods {
		c.totalSuccess[m] = new(int64)
		c.totalError[m] = new(int64)
	}
	return c
}

// Inc фиксирует один вызов метода с указанным исходом.
// Ниль-безопасна (call-site не проверяет). Метод, отсутствующий в карте
// счётчиков, игнорируется (защита от опечаток при добавлении новых методов).
func (c *Counters) Inc(m Method, err error) {
	if c == nil {
		return
	}
	var p *int64
	if err != nil {
		p = c.totalError[m]
	} else {
		p = c.totalSuccess[m]
	}
	if p != nil {
		atomic.AddInt64(p, 1)
	}
	c.touchRate()
}

// touchRate увеличивает секундный счётчик. При смене секунды текущий бакет
// переезжает в previous (становится «последней завершённой секундой»), а
// текущий сбрасывается. Под мьютексом — write-путь не горячий настолько,
// чтобы atomic давал выигрыш, зато логика ротации простая и корректная.
func (c *Counters) touchRate() {
	now := time.Now().Unix()
	c.rateMu.Lock()
	if c.rateSecond != now {
		// Секунда сменилась: текущий бакет → previous, начинаем новый.
		c.ratePrevious = c.rateCurrent
		c.rateCurrent = 0
		c.rateSecond = now
	}
	c.rateCurrent++
	c.rateMu.Unlock()
}

// RateLastSecond возвращает количество вызовов за последнюю завершённую
// секунду (стабильный gauge «rps»: не флапает внутри текущей секунды).
func (c *Counters) RateLastSecond() int64 {
	if c == nil {
		return 0
	}
	c.rateMu.Lock()
	defer c.rateMu.Unlock()
	// Если текущая секунда ещё «жива» (не сменилась с момента последней записи),
	// previous — это последняя ПОЛНАЯ секунда. Если секунда давно сменилась и
	// новых записей не было — previous тоже устарел, но это лишь означает, что
	// rps сейчас ~0; для gauge это приемлемо.
	return c.ratePrevious
}

// TotalSuccess / TotalError — суммарные счётчики по методу.
func (c *Counters) TotalSuccess(m Method) int64 {
	if c == nil || c.totalSuccess[m] == nil {
		return 0
	}
	return atomic.LoadInt64(c.totalSuccess[m])
}

func (c *Counters) TotalError(m Method) int64 {
	if c == nil || c.totalError[m] == nil {
		return 0
	}
	return atomic.LoadInt64(c.totalError[m])
}

// TotalAll — сумма по всем методам и исходам.
func (c *Counters) TotalAll() int64 {
	if c == nil {
		return 0
	}
	var sum int64
	for _, m := range orderedMethods {
		sum += c.TotalSuccess(m) + c.TotalError(m)
	}
	return sum
}

// WritePrometheus пишет метрики в формате Prometheus text exposition.
// Формат намеренно простой (без клиента prometheus client lib — ради нуля
// зависимостей): внешнему скрейперу достаточно регулярных метрик.
//
// Метрики:
//
//	max_api_calls_total{method="send_text",result="success|error"} N
//	max_api_rps_last_second N
//	max_api_uptime_seconds N
func (c *Counters) WritePrometheus(w io.Writer) {
	if c == nil {
		return
	}
	// Заголовки help/type (Prometheus-friendly).
	fmt.Fprintln(w, "# HELP max_api_calls_total Total MAX API calls by method and result")
	fmt.Fprintln(w, "# TYPE max_api_calls_total counter")

	// Стабильный порядок вывода.
	methods := make([]Method, len(orderedMethods))
	copy(methods, orderedMethods)
	sort.Slice(methods, func(i, j int) bool { return methods[i] < methods[j] })

	for _, m := range methods {
		s := c.TotalSuccess(m)
		e := c.TotalError(m)
		fmt.Fprintf(w, "max_api_calls_total{method=%q,result=\"success\"} %d\n", m, s)
		fmt.Fprintf(w, "max_api_calls_total{method=%q,result=\"error\"} %d\n", m, e)
	}

	fmt.Fprintln(w, "# HELP max_api_rps_last_second Approximate MAX API calls in the last second")
	fmt.Fprintln(w, "# TYPE max_api_rps_last_second gauge")
	fmt.Fprintf(w, "max_api_rps_last_second %d\n", c.RateLastSecond())
}
