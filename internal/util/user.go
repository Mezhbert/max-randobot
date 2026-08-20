package util

import (
	"fmt"
	"strings"
)

// DisplayName — как показывать пользователя в списках (победители и т.п.).
// Приоритет: @username (список победителей — по юзернеймам, юзернейм
// однозначен и кликабелен в MAX) → имя → безымянный fallback по id.
func DisplayName(userID int64, username, firstName string) string {
	if username != "" {
		return "@" + strings.TrimPrefix(username, "@")
	}
	if firstName != "" {
		return firstName
	}
	return fmt.Sprintf("Участник #%d", userID)
}
