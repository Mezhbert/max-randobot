// Package captcha — генерация и проверка image-капчи (PNG с цифрами).
// Использует github.com/dchest/captcha (чистый Go, без внешних сервисов).
// Состояние (ожидаемый ответ) хранится in-memory с TTL (один инстанс бота).
//
// Флоу: Generate(userID) → PNG-байты + ответ запоминается; Verify(userID, ответ)
// проверяет и очищает. При истечении TTL запись удаляется (lazy).
package captcha

import (
	"bytes"
	"sync"
	"time"

	"github.com/dchest/captcha"
)

// DefaultTTL — срок действия капчи (пользователь должен ввести ответ за это время).
const DefaultTTL = 5 * time.Minute

// const digitsLen — длина кода (4 цифры — баланс сложности/читаемости).
const digitsLen = 4

// pending — ожидаемая запись (ответ + момент истечения).
type pending struct {
	answer   string
	expireAt int64 // unix-nano
}

// Service — генератор/верификатор капчи. Потокобезопасен.
type Service struct {
	mu      sync.Mutex
	pending map[int64]pending // userID → ожидающая капча
	ttl     time.Duration
}

// New создаёт сервис с заданным TTL (0 → DefaultTTL).
func New(ttl time.Duration) *Service {
	if ttl <= 0 {
		ttl = DefaultTTL
	}
	return &Service{pending: make(map[int64]pending), ttl: ttl}
}

// Generate создаёт новую капчу для пользователя, возвращает PNG-байты картинки.
// Ответ (цифры) запоминается и будет проверен в Verify.
func (s *Service) Generate(userID int64) ([]byte, error) {
	digits := captcha.RandomDigits(digitsLen)
	answer := digitsString(digits)

	pngBuf := bytes.NewBuffer(nil)
	if _, err := captcha.NewImage("", digits, 240, 80).WriteTo(pngBuf); err != nil {
		return nil, err
	}

	s.mu.Lock()
	s.pending[userID] = pending{
		answer:   answer,
		expireAt: time.Now().Add(s.ttl).UnixNano(),
	}
	s.mu.Unlock()

	return pngBuf.Bytes(), nil
}

// Verify проверяет ответ пользователя. Возвращает true при совпадении.
// При любой проверке (успешной или нет) запись удаляется — повторная попытка
// требует новой капчи (защита от перебора).
func (s *Service) Verify(userID int64, answer string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()

	p, ok := s.pending[userID]
	delete(s.pending, userID) // всегда удаляем — одна попытка
	if !ok {
		return false // нет ожидающей капчи (истекла или не генерировалась)
	}
	if time.Now().UnixNano() > p.expireAt {
		return false // истекла
	}
	return p.answer == answer
}

// Has — есть ли ожидающая капча для пользователя (не истёкшая).
func (s *Service) Has(userID int64) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	p, ok := s.pending[userID]
	if !ok {
		return false
	}
	return time.Now().UnixNano() <= p.expireAt
}

// digitsString конвертирует []byte цифр в строку ("1234").
func digitsString(d []byte) string {
	b := make([]byte, len(d))
	for i, c := range d {
		b[i] = byte('0') + c
	}
	return string(b)
}
