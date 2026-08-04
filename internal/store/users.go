package store

import (
	"context"
	"errors"
)

// ErrNotFound — запись не найдена.
var ErrNotFound = errors.New("not found")

// UpsertUser создаёт/обновляет пользователя. flag_in_base сохраняет предыдущее
// значение при обновлении (меняется только явным SetFlagInBase).
func (s *Store) UpsertUser(ctx context.Context, u User) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (user_id, first_name, username, is_bot, flag_in_base)
		VALUES ($1, $2, $3, $4, TRUE)
		ON CONFLICT (user_id) DO UPDATE
		  SET first_name = EXCLUDED.first_name,
		      username   = EXCLUDED.username,
		      is_bot     = EXCLUDED.is_bot
	`, u.UserID, u.FirstName, u.Username, u.IsBot)
	return err
}

// SetFlagInBase явно выставляет флаг "можно ли писать пользователю".
// Вызывать с false, если бот узнал, что юзер заблокировал диалог.
func (s *Store) SetFlagInBase(ctx context.Context, userID int64, v bool) error {
	_, err := s.pool.Exec(ctx, `UPDATE users SET flag_in_base=$2 WHERE user_id=$1`, userID, v)
	return err
}

// GetUser возвращает пользователя по id.
func (s *Store) GetUser(ctx context.Context, userID int64) (User, error) {
	var u User
	err := s.pool.QueryRow(ctx, `
		SELECT user_id, first_name, username, is_bot, flag_in_base, created_at, updated_at
		FROM users WHERE user_id=$1
	`, userID).Scan(&u.UserID, &u.FirstName, &u.Username, &u.IsBot, &u.FlagInBase, &u.CreatedAt, &u.UpdatedAt)
	if err != nil {
		return u, wrapNotFound(err)
	}
	return u, nil
}
