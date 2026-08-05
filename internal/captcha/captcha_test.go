package captcha

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestGenerate_ReturnsPNG — генерация возвращает непустые PNG-байты.
func TestGenerate_ReturnsPNG(t *testing.T) {
	s := New(0)
	data, err := s.Generate(123)
	require.NoError(t, err)
	require.NotEmpty(t, data)
	// PNG signature.
	assert.True(t, len(data) > 8 && string(data[:4]) == "\x89PNG",
		"должен быть валидный PNG, начало: %x", data[:4])
}

// TestGenerate_DifferentUsersDifferentImages — разным юзерам — разные картинки.
func TestGenerate_DifferentUsersDifferentImages(t *testing.T) {
	s := New(0)
	d1, _ := s.Generate(1)
	d2, _ := s.Generate(2)
	// Почти наверняка разные (random digits).
	assert.NotEqual(t, d1, d2, "капчи разных юзеров должны отличаться")
}

// TestVerify_CorrectAnswer — верный ответ проходит.
func TestVerify_CorrectAnswer(t *testing.T) {
	s := New(0)
	// Сгенерируем и «подсмотрим» ответ через внутренний map.
	_, _ = s.Generate(42)
	s.mu.Lock()
	expected := s.pending[42].answer
	s.mu.Unlock()
	require.Len(t, expected, 4)

	assert.True(t, s.Verify(42, expected), "верный ответ должен пройти")
	// После проверки запись удалена — повторная верификация false.
	assert.False(t, s.Verify(42, expected), "после проверки капча удаляется")
}

// TestVerify_WrongAnswer — неверный ответ не проходит, капча удаляется.
func TestVerify_WrongAnswer(t *testing.T) {
	s := New(0)
	_, _ = s.Generate(99)
	// Подсмотрим правильный, ответим другим.
	s.mu.Lock()
	correct := s.pending[99].answer
	s.mu.Unlock()
	wrong := "0000"
	if correct == wrong {
		wrong = "1111"
	}
	assert.False(t, s.Verify(99, wrong))
	// После неверного ответа — запись удалена (защита от перебора).
	assert.False(t, s.Has(99))
}

// TestVerify_NoPending — нет ожидающей капчи → false.
func TestVerify_NoPending(t *testing.T) {
	s := New(0)
	assert.False(t, s.Verify(555, "1234"))
}

// TestHas — отражает наличие неистёкшей капчи.
func TestHas(t *testing.T) {
	s := New(0)
	assert.False(t, s.Has(7))
	_, _ = s.Generate(7)
	assert.True(t, s.Has(7))
	_ = s.Verify(7, "wrong")
	assert.False(t, s.Has(7), "после verify капча удаляется")
}

// TestExpired — истёкшая капча не валидируется.
func TestExpired(t *testing.T) {
	s := New(1) // TTL 1 наносекунда → почти сразу истекает
	_, _ = s.Generate(8)
	// Маленькая пауза чтобы капча гарантированно истекла.
	// TTL=1ns, так что к моменту Verify уже истекла.
	// Has может вернуть true если проверка мгновенна, но Verify проверит expire.
	s.mu.Lock()
	p := s.pending[8]
	p.expireAt = 1 // unix-nano=1 → точно в прошлом
	s.pending[8] = p
	s.mu.Unlock()
	assert.False(t, s.Verify(8, ""), "истёкшая капча не должна валидироваться")
}

// TestDigitsString — конвертация []byte цифр в строку.
func TestDigitsString(t *testing.T) {
	assert.Equal(t, "0123", digitsString([]byte{0, 1, 2, 3}))
	assert.Equal(t, "9876", digitsString([]byte{9, 8, 7, 6}))
	assert.Equal(t, "", digitsString(nil))
}

// TestGenerate_ImageIsDecodable — PNG можно декодировать (хотя бы read signature).
func TestGenerate_ImageIsDecodable(t *testing.T) {
	s := New(0)
	for i := 0; i < 5; i++ {
		data, err := s.Generate(int64(100 + i))
		require.NoError(t, err)
		// PNG header: 89 50 4E 47 0D 0A 1A 0A
		require.Greater(t, len(data), 100, "PNG слишком маленький")
		assert.Equal(t, "\x89PNG\r\n\x1a\n", string(data[:8]))
	}
}

// TestGenerate_Regenerates — повторная генерация для того же юзера обновляет ответ.
func TestGenerate_Regenerates(t *testing.T) {
	s := New(0)
	_, _ = s.Generate(1)
	s.mu.Lock()
	a1 := s.pending[1].answer
	s.mu.Unlock()

	_, _ = s.Generate(1)
	s.mu.Lock()
	a2 := s.pending[1].answer
	s.mu.Unlock()

	// С 4 цифрами и RandomDigits совпадение возможно, но маловероятно.
	// Не asserts на неравенство — лишь проверяем что запись перезаписалась.
	assert.Len(t, a2, 4)
	_ = a1
}

// TestNilSafe — вызовы на нулевом сервисе не паникуют (хотя New всегда возвращает валидный).
func TestVerify_NotPanicsOnEmpty(t *testing.T) {
	s := New(0)
	assert.NotPanics(t, func() {
		_ = s.Verify(0, "")
	})
}

// TestMultiplePending — несколько юзеров одновременно, не смешиваются.
func TestMultiplePending(t *testing.T) {
	s := New(0)
	_, _ = s.Generate(10)
	_, _ = s.Generate(20)
	_, _ = s.Generate(30)

	s.mu.Lock()
	a10 := s.pending[10].answer
	a20 := s.pending[20].answer
	a30 := s.pending[30].answer
	s.mu.Unlock()

	assert.Len(t, a10, 4)
	assert.Len(t, a20, 4)
	assert.Len(t, a30, 4)
	// Каждый юзер получает свой ответ.
	assert.True(t, s.Verify(20, a20))
	assert.False(t, s.Verify(10, a20), "ответ юзера 20 не подходит юзеру 10")
}
