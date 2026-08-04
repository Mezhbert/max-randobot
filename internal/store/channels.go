package store

import (
	"context"
	"strings"
)

// AddChannel добавляет канал в "мои каналы" пользователя.
// Перед этим бот должен быть админом в канале (проверяется в слое giveaway).
func (s *Store) AddChannel(ctx context.Context, c Channel) (Channel, error) {
	var out Channel
	err := s.pool.QueryRow(ctx, `
		INSERT INTO channels (owner_user_id, chat_id, title, link, is_public, is_bot_admin)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (owner_user_id, chat_id) DO UPDATE
		  SET title        = EXCLUDED.title,
		      link         = EXCLUDED.link,
		      is_bot_admin = EXCLUDED.is_bot_admin
		RETURNING id, owner_user_id, chat_id, title, link, is_public, is_bot_admin, created_at, updated_at
	`, c.OwnerUserID, c.ChatID, c.Title, c.Link, c.IsPublic, c.IsBotAdmin,
	).Scan(&out.ID, &out.OwnerUserID, &out.ChatID, &out.Title, &out.Link, &out.IsPublic,
		&out.IsBotAdmin, &out.CreatedAt, &out.UpdatedAt)
	return out, err
}

// ListChannels возвращает каналы пользователя ("мои каналы").
func (s *Store) ListChannels(ctx context.Context, ownerUserID int64) ([]Channel, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, owner_user_id, chat_id, title, link, is_public, is_bot_admin, admin_checked_at, created_at, updated_at
		FROM channels WHERE owner_user_id=$1 ORDER BY created_at
	`, ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.OwnerUserID, &c.ChatID, &c.Title, &c.Link,
			&c.IsPublic, &c.IsBotAdmin, &c.AdminCheckedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		res = append(res, c)
	}
	return res, rows.Err()
}

// GetChannel — по внутреннему id.
func (s *Store) GetChannel(ctx context.Context, id, ownerUserID int64) (Channel, error) {
	var c Channel
	err := s.pool.QueryRow(ctx, `
		SELECT id, owner_user_id, chat_id, title, link, is_public, is_bot_admin, admin_checked_at, created_at, updated_at
		FROM channels WHERE id=$1 AND owner_user_id=$2
	`, id, ownerUserID).Scan(&c.ID, &c.OwnerUserID, &c.ChatID, &c.Title, &c.Link,
		&c.IsPublic, &c.IsBotAdmin, &c.AdminCheckedAt, &c.CreatedAt, &c.UpdatedAt)
	return c, wrapNotFound(err)
}

// GetChannelByChat — по chat_id MAX (для публикации/проверки).
func (s *Store) GetChannelByChat(ctx context.Context, ownerUserID, chatID int64) (Channel, error) {
	var c Channel
	err := s.pool.QueryRow(ctx, `
		SELECT id, owner_user_id, chat_id, title, link, is_public, is_bot_admin, admin_checked_at, created_at, updated_at
		FROM channels WHERE chat_id=$1 AND owner_user_id=$2
	`, chatID, ownerUserID).Scan(&c.ID, &c.OwnerUserID, &c.ChatID, &c.Title, &c.Link,
		&c.IsPublic, &c.IsBotAdmin, &c.AdminCheckedAt, &c.CreatedAt, &c.UpdatedAt)
	return c, wrapNotFound(err)
}

// SetChannelAdminFlag обновляет кэш факта, что бот админ в канале.
func (s *Store) SetChannelAdminFlag(ctx context.Context, id int64, isAdmin bool) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE channels SET is_bot_admin=$2, admin_checked_at=now() WHERE id=$1`, id, isAdmin)
	return err
}

// ChannelsByIDs возвращает каналы по списку внутренних id (для публикаций).
func (s *Store) ChannelsByIDs(ctx context.Context, ownerUserID int64, ids []int64) ([]Channel, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	// ids передаются как строка через ANY для безопасности.
	placeholders := make([]string, len(ids))
	args := make([]any, 0, len(ids)+1)
	args = append(args, ownerUserID)
	for i, id := range ids {
		args = append(args, id)
		placeholders[i] = "$" + itoa(i+2)
	}
	q := `SELECT id, owner_user_id, chat_id, title, link, is_public, is_bot_admin, admin_checked_at, created_at, updated_at
	      FROM channels WHERE owner_user_id=$1 AND id IN (` + strings.Join(placeholders, ",") + `)`
	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []Channel
	for rows.Next() {
		var c Channel
		if err := rows.Scan(&c.ID, &c.OwnerUserID, &c.ChatID, &c.Title, &c.Link,
			&c.IsPublic, &c.IsBotAdmin, &c.AdminCheckedAt, &c.CreatedAt, &c.UpdatedAt); err != nil {
			return nil, err
		}
		res = append(res, c)
	}
	return res, rows.Err()
}

// LogChannelJoin — запись о вступлении юзера в канал (по chat_id MAX).
// Связывает юзера с каналом по chat_id среди всех каналов-владельцев.
// source: 'post' | 'utm'.
func (s *Store) LogChannelJoin(ctx context.Context, userID, chatID int64, source string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO subscriptions_log (participant_id, giveaway_id, channel_id, user_id, sub_source)
		SELECT 0, 0, c.id, $1, $2
		FROM channels c
		WHERE c.chat_id = $3
	`, userID, source, chatID)
	return err
}

// MarkUnsubscribedByChat — отписка юзера от канала по chat_id MAX.
func (s *Store) MarkUnsubscribedByChat(ctx context.Context, userID, chatID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE subscriptions_log sl
		SET unsubscribed_at = now()
		FROM channels c
		WHERE sl.user_id=$1 AND c.chat_id=$2 AND sl.channel_id = c.id
		  AND sl.unsubscribed_at IS NULL
	`, userID, chatID)
	return err
}
