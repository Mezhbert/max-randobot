// Package dedup — дедупликация входящих webhook-апдейтов.
//
// MAX ретраит webhook-запрос, если бот не ответил 200 достаточно быстро. Так как
// обработка идёт асинхронно (cmd/bot крутит `go bot.Handle(...)`), один и тот же
// апдейт может быть обработан дважды → дубли меню/ответов. Этот пакет даёт
// идемпотентный приём: повторный апдейт с тем же ключом отбрасывается.
//
// Реализация — in-process map с TTL (достаточно для одного инстанса). За
// интерфейсом SeenOrMark при мульти-инстансе встанет Redis без изменения
// call-site. Пока намеренно без внешних зависимостей.
package dedup

import (
	"sync"
	"time"

	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// DefaultTTL — срок, в течение которого апдейт считается дублем. Достаточно
// большой, чтобы покрыть ретраи платформы, но не копить данные бесконечно.
const DefaultTTL = 5 * time.Minute

// Deduplicator — идемпотентный приём апдейтов. Потокобезопасен.
type Deduplicator struct {
	mu      sync.Mutex
	seen    map[string]int64 // key → unix-nano момента истечения
	ttl     time.Duration
	maxSize int
}

// New создаёт дедупликатор с заданным TTL. maxSize ограничивает рост (0 → 50000).
func New(ttl time.Duration, maxSize int) *Deduplicator {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	if maxSize <= 0 {
		maxSize = 50000
	}
	return &Deduplicator{seen: make(map[string]int64), ttl: ttl, maxSize: maxSize}
}

// SeenOrMark атомарно: если ключ уже видели — возвращает true (дубль, отбросить);
// иначе отмечает ключ и возвращает false (первая обработка, продолжить).
func (d *Deduplicator) SeenOrMark(key string) bool {
	if key == "" {
		return false // нечего дедупить — пропускаем как уникальное
	}
	now := time.Now().UnixNano()
	expire := now + int64(d.ttl)

	d.mu.Lock()
	defer d.mu.Unlock()

	if prev, ok := d.seen[key]; ok && prev > now {
		return true // уже видели и ещё активно
	}
	// Lazy eviction при достижении потолка: чистим протухшие.
	if len(d.seen) >= d.maxSize {
		for k, exp := range d.seen {
			if exp <= now {
				delete(d.seen, k)
			}
		}
	}
	d.seen[key] = expire
	return false
}

// Key для апдейта: уникален в пределах типа события.
// Возвращает "" если для данного апдейта нет надёжного идентификатора
// (например, bot_started без messageID) — тогда дедуп пропускается.
//
// Используемые идентификаторы:
//   - message_created/edited/removed: MessageID (глобально уникален)
//   - message_callback: CallbackID (глобально уникален)
//   - user_added/user_removed/bot_started/bot_stopped: "<type>:<userID>:<chatID>:<ts>"
//     (timestamp с миллисекундами; для редких событий достаточен против ретраев)
func Key(u model.Update) string {
	switch u.UpdateType {
	case model.UpdateMessageCreated, model.UpdateMessageEdited, model.UpdateMessageRemoved:
		if u.MessageID != "" {
			return "m:" + u.MessageID
		}
	case model.UpdateMessageCallback:
		cb := u.GetCallback()
		if cb.CallbackID != "" {
			return "c:" + cb.CallbackID
		}
	}
	// Прочие события (старт/останов/вступление/выход) — композитный ключ.
	if u.UserID == 0 && u.ChatID == 0 && u.Timestamp == 0 {
		return ""
	}
	return string(u.UpdateType) + ":" +
		itoa(u.UserID) + ":" + itoa(u.ChatID) + ":" + itoa(u.Timestamp)
}

func itoa(n int64) string {
	// Внутренний, чтобы не тянуть strconv ради одной функции.
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var b [20]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		b[i] = '-'
	}
	return string(b[i:])
}
