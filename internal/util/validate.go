package util

import (
	"errors"
	"fmt"
	"strings"
	"unicode/utf8"
)

// Лимиты пользовательского текста (в рунах, не байтах — текст русский).
// Граница ввода — HandleTextInput (FSM) и обработчики сессий (edit/UTM);
// всё, что длиннее, отвергается сразу, а не при публикации поста.
const (
	MaxNameLen        = 128  // название розыгрыша (шаг 1)
	MaxPostTextLen    = 4000 // текст поста (шаг 2)
	MaxButtonTextLen  = 64   // текст кнопки участия (шаг 4)
	MaxWinnersMsgLen  = 3500 // сообщение победителям (шаг 12)
	MaxResultsTextLen = 3500 // текст отдельного поста с итогами
	MaxUTMSourceLen   = 64   // название источника UTM-ссылки
)

// ValidatePostText — обязательный текст поста: непустой и в пределах лимита.
func ValidatePostText(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("текст поста не может быть пустым. Отправьте текст сообщения — фото/видео добавляются на следующем шаге")
	}
	return checkLen("Текст поста", s, MaxPostTextLen)
}

// ValidateName — обязательное название розыгрыша.
func ValidateName(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("название не может быть пустым")
	}
	return checkLen("Название", s, MaxNameLen)
}

// ValidateButtonText — текст кнопки участия: одна строка, короткий.
func ValidateButtonText(s string) error {
	if strings.ContainsAny(s, "\n\r") {
		return errors.New("текст кнопки должен быть одной строкой без переносов")
	}
	return checkLen("Текст кнопки", s, MaxButtonTextLen)
}

// ValidateLongText — опциональное длинное сообщение (победителям / текст итогов).
// Пустое допустимо (шаг пропускается), проверяется только длина.
func ValidateLongText(what, s string) error {
	return checkLen(what, s, MaxWinnersMsgLen)
}

// ValidateUTMSource — название источника трекинг-ссылки: непустое, одна строка.
func ValidateUTMSource(s string) error {
	if strings.TrimSpace(s) == "" {
		return errors.New("название не может быть пустым. Отправьте название источника")
	}
	if strings.ContainsAny(s, "\n\r") {
		return errors.New("название источника должно быть одной строкой без переносов")
	}
	return checkLen("Название источника", s, MaxUTMSourceLen)
}

// ValidatePostRef — пост-источник трекинг-ссылки (куда ссылка будет размещена):
// одна строка (ссылка на пост или описание «Сторис от 15.10»), до 128 символов.
func ValidatePostRef(s string) error {
	if strings.ContainsAny(s, "\n\r") {
		return errors.New("пост-источник должен быть одной строкой: ссылкой на пост или коротким описанием")
	}
	return checkLen("Пост-источник", s, 128)
}

// checkLen — длина в рунах в пределах max. Сообщение готово для показа юзеру.
func checkLen(what, s string, max int) error {
	if n := utf8.RuneCountInString(s); n > max {
		return fmt.Errorf("%s: слишком длинный (%d символов) — максимум %d", what, n, max)
	}
	return nil
}
