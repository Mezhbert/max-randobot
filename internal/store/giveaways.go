package store

import (
	"context"
	"encoding/json"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// CreateDraftGiveaway создаёт новый черновик розыгрыша для пользователя.
// Если уже есть активный черновик в режиме создания — возвращаем его (один черновик на сессию).
func (s *Store) CreateDraftGiveaway(ctx context.Context, ownerUserID int64, step string) (Giveaway, error) {
	var g Giveaway
	err := s.pool.QueryRow(ctx, `
		INSERT INTO giveaways (owner_user_id, draft_step, status)
		VALUES ($1, $2, 'draft')
		RETURNING id, owner_user_id, name, post_text, post_format, media_tokens, button_text,
		          subscribe_channel_ids, publish_channel_ids, results_in_original_post, results_post_text,
		          publish_at, finish_at, winners_count, winners_message,
		          referral_enabled, referral_limit, captcha_mode, captcha_percent, promotion_enabled,
		          draft_step, status, publish_message_ids, results_message_ids, winners_user_ids, created_at, updated_at
	`, ownerUserID, step,
	).Scan(&g.ID, &g.OwnerUserID, &g.Name, &g.PostText, &g.PostFormat, &g.MediaTokens, &g.ButtonText,
		&g.SubscribeChannelIDs, &g.PublishChannelIDs, &g.ResultsInOriginalPost, &g.ResultsPostText,
		&g.PublishAt, &g.FinishAt, &g.WinnersCount, &g.WinnersMessage,
		&g.ReferralEnabled, &g.ReferralLimit, &g.CaptchaMode, &g.CaptchaPercent, &g.PromotionEnabled,
		&g.DraftStep, &g.Status, &g.PublishMessageIDs, &g.ResultsMessageIDs, &g.WinnersUserIDs, &g.CreatedAt, &g.UpdatedAt)
	if err != nil {
		return g, err
	}
	return g, nil
}

// GetDraftGiveaway возвращает активный черновик пользователя (в режиме создания).
func (s *Store) GetDraftGiveaway(ctx context.Context, ownerUserID int64) (Giveaway, error) {
	var g Giveaway
	err := s.scanGiveaway(s.pool.QueryRow(ctx, `
		SELECT `+giveawayCols+` FROM giveaways
		WHERE owner_user_id=$1 AND status='draft' AND draft_step IS NOT NULL
		ORDER BY updated_at DESC LIMIT 1
	`, ownerUserID), &g)
	return g, err
}

// GetGiveaway — по id с проверкой владельца.
func (s *Store) GetGiveaway(ctx context.Context, id, ownerUserID int64) (Giveaway, error) {
	var g Giveaway
	err := s.scanGiveaway(s.pool.QueryRow(ctx, `
		SELECT `+giveawayCols+` FROM giveaways WHERE id=$1 AND owner_user_id=$2
	`, id, ownerUserID), &g)
	return g, err
}

// GetGiveawayAny — без проверки владельца (для участников/публикации).
func (s *Store) GetGiveawayAny(ctx context.Context, id int64) (Giveaway, error) {
	var g Giveaway
	err := s.scanGiveaway(s.pool.QueryRow(ctx, `
		SELECT `+giveawayCols+` FROM giveaways WHERE id=$1
	`, id), &g)
	return g, err
}

// ListGiveaways — розыгрыши пользователя по статусу (для экрана "мои розыгрыши").
// status="" — все.
func (s *Store) ListGiveaways(ctx context.Context, ownerUserID int64, status string) ([]Giveaway, error) {
	q := `SELECT ` + giveawayCols + ` FROM giveaways WHERE owner_user_id=$1`
	args := []any{ownerUserID}
	if status != "" {
		q += ` AND status=$2`
		args = append(args, status)
	}
	q += ` ORDER BY updated_at DESC`

	rows, err := s.pool.Query(ctx, q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []Giveaway
	for rows.Next() {
		var g Giveaway
		if err := s.scanGiveawayRow(rows, &g); err != nil {
			return nil, err
		}
		res = append(res, g)
	}
	return res, rows.Err()
}

// ListMyParticipations — розыгрыши, в которых пользователь участник.
func (s *Store) ListMyParticipations(ctx context.Context, userID int64) ([]Giveaway, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+giveawayColsAlias("g")+` FROM giveaways g
		JOIN participants p ON p.giveaway_id = g.id
		WHERE p.user_id=$1
		ORDER BY p.joined_at DESC
	`, userID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var res []Giveaway
	for rows.Next() {
		var g Giveaway
		if err := s.scanGiveawayRow(rows, &g); err != nil {
			return nil, err
		}
		res = append(res, g)
	}
	return res, rows.Err()
}

// UpdateGiveawayField — точечное обновление одного поля черновика в процессе FSM.
// field — имя колонки, val — значение. Безопасно: field из белого списка FSM.
func (s *Store) UpdateGiveawayField(ctx context.Context, id int64, field string, val any) error {
	allowed := map[string]bool{
		"post_text": true, "post_format": true, "media_tokens": true, "button_text": true,
		"subscribe_channel_ids": true, "publish_channel_ids": true,
		"results_in_original_post": true, "results_post_text": true,
		"publish_at": true, "finish_at": true, "winners_count": true, "winners_message": true,
		"referral_enabled": true, "referral_limit": true,
		"captcha_mode": true, "captcha_percent": true, "promotion_enabled": true,
		"draft_step": true, "name": true,
	}
	if !allowed[field] {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE giveaways SET `+field+`=$2 WHERE id=$1 AND status='draft'`, id, val)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// SetDraftStep обновляет шаг FSM черновика.
func (s *Store) SetDraftStep(ctx context.Context, id int64, step string) error {
	return s.UpdateGiveawayField(ctx, id, "draft_step", step)
}

// SaveDraftAsDraft — пользователь нажал "Отменить": черновик остаётся draft,
// но draft_step сбрасывается (выход из режима создания). По ТЗ отменённый
// розыгрыш сохраняется как черновик.
func (s *Store) AbandonDraft(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE giveaways SET draft_step=NULL WHERE id=$1 AND status='draft'`, id)
	return err
}

// PublishDraft — подтверждение создания: статус → scheduled (если publish_at в будущем)
// или active (если уже пора). draft_step сбрасывается.
func (s *Store) PublishDraft(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE giveaways
		SET status = CASE
		        WHEN publish_at IS NOT NULL AND publish_at > now() THEN 'scheduled'
		        ELSE 'active'
		    END,
		    draft_step = NULL
		WHERE id=$1 AND status='draft'
	`, id)
	return err
}

// SetStatus — смена статуса (cancel/finish).
func (s *Store) SetStatus(ctx context.Context, id int64, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE giveaways SET status=$2 WHERE id=$1`, id, status)
	return err
}

// DeleteGiveaway удаляет розыгрыш (черновики — по ТЗ можно удалять).
func (s *Store) DeleteGiveaway(ctx context.Context, id, ownerUserID int64) error {
	tag, err := s.pool.Exec(ctx,
		`DELETE FROM giveaways WHERE id=$1 AND owner_user_id=$2 AND status!='finished'`, id, ownerUserID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ScheduledReady — запланированные розыгрыши, у которых наступило publish_at.
func (s *Store) ScheduledReady(ctx context.Context, now time.Time) ([]Giveaway, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+giveawayCols+` FROM giveaways
		WHERE status='scheduled' AND publish_at IS NOT NULL AND publish_at<=$1
		ORDER BY publish_at
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.scanGiveaways(rows)
}

// ActiveFinished — текущие розыгрыши, у которых наступило finish_at.
func (s *Store) ActiveFinished(ctx context.Context, now time.Time) ([]Giveaway, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+giveawayCols+` FROM giveaways
		WHERE status='active' AND finish_at IS NOT NULL AND finish_at<=$1
		ORDER BY finish_at
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.scanGiveaways(rows)
}

// StorePublishMessageID сохраняет mid опубликованного сообщения в канале.
func (s *Store) StorePublishMessageID(ctx context.Context, id, chatID int64, mid string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE giveaways
		SET publish_message_ids = publish_message_ids || jsonb_build_object($2::text, $3)
		WHERE id=$1
	`, id, chatID, mid)
	return err
}

// StoreResultsMessageID — mid поста с итогами.
func (s *Store) StoreResultsMessageID(ctx context.Context, id, chatID int64, mid string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE giveaways
		SET results_message_ids = results_message_ids || jsonb_build_object($2::text, $3)
		WHERE id=$1
	`, id, chatID, mid)
	return err
}

// SaveWinners — фиксируем победителей.
func (s *Store) SaveWinners(ctx context.Context, id int64, winnerIDs []int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE giveaways SET winners_user_ids=$2 WHERE id=$1`, id, winnerIDs)
	return err
}

// ---------- сканеры ----------

const giveawayCols = `id, owner_user_id, name, post_text, post_format, media_tokens, button_text,
	subscribe_channel_ids, publish_channel_ids, results_in_original_post, results_post_text,
	publish_at, finish_at, winners_count, winners_message,
	referral_enabled, referral_limit, captcha_mode, captcha_percent, promotion_enabled,
	draft_step, status, publish_message_ids, results_message_ids, winners_user_ids, created_at, updated_at`

func giveawayColsAlias(alias string) string {
	// подставляет префикс алиаса к каждой колонке — для JOIN'ов.
	cols := []string{
		"id", "owner_user_id", "name", "post_text", "post_format", "media_tokens", "button_text",
		"subscribe_channel_ids", "publish_channel_ids", "results_in_original_post", "results_post_text",
		"publish_at", "finish_at", "winners_count", "winners_message",
		"referral_enabled", "referral_limit", "captcha_mode", "captcha_percent", "promotion_enabled",
		"draft_step", "status", "publish_message_ids", "results_message_ids", "winners_user_ids", "created_at", "updated_at",
	}
	out := ""
	for i, c := range cols {
		if i > 0 {
			out += ", "
		}
		out += alias + "." + c
	}
	return out
}

type rowScanner interface {
	Scan(dest ...any) error
}

func (s *Store) scanGiveaway(row rowScanner, g *Giveaway) error {
	if err := s.scanRow(row, g); err != nil {
		return wrapNotFound(err)
	}
	return nil
}

type rowIter interface {
	Scan(dest ...any) error
	Next() bool
	Err() error
}

func (s *Store) scanGiveawayRow(rows rowIter, g *Giveaway) error { return s.scanRow(rows, g) }

func (s *Store) scanRow(sc interface{ Scan(dest ...any) error }, g *Giveaway) error {
	var publishAt, finishAt pgtype.Timestamptz
	var mediaTokens []byte
	var publishMsgs, resultsMsgs []byte
	err := sc.Scan(
		&g.ID, &g.OwnerUserID, &g.Name, &g.PostText, &g.PostFormat, &mediaTokens, &g.ButtonText,
		&g.SubscribeChannelIDs, &g.PublishChannelIDs, &g.ResultsInOriginalPost, &g.ResultsPostText,
		&publishAt, &finishAt, &g.WinnersCount, &g.WinnersMessage,
		&g.ReferralEnabled, &g.ReferralLimit, &g.CaptchaMode, &g.CaptchaPercent, &g.PromotionEnabled,
		&g.DraftStep, &g.Status, &publishMsgs, &resultsMsgs, &g.WinnersUserIDs, &g.CreatedAt, &g.UpdatedAt,
	)
	if err != nil {
		return err
	}
	if publishAt.Valid {
		g.PublishAt = &publishAt.Time
	}
	if finishAt.Valid {
		g.FinishAt = &finishAt.Time
	}
	if g.MediaTokens == nil {
		g.MediaTokens = []MediaToken{}
	}
	_ = json.Unmarshal(mediaTokens, &g.MediaTokens)
	if g.PublishMessageIDs == nil {
		g.PublishMessageIDs = map[int64]string{}
	}
	_ = json.Unmarshal(publishMsgs, &g.PublishMessageIDs)
	if g.ResultsMessageIDs == nil {
		g.ResultsMessageIDs = map[int64]string{}
	}
	_ = json.Unmarshal(resultsMsgs, &g.ResultsMessageIDs)
	if g.SubscribeChannelIDs == nil {
		g.SubscribeChannelIDs = []int64{}
	}
	if g.PublishChannelIDs == nil {
		g.PublishChannelIDs = []int64{}
	}
	if g.WinnersUserIDs == nil {
		g.WinnersUserIDs = []int64{}
	}
	return nil
}

func (s *Store) scanGiveaways(rows pgxRows) ([]Giveaway, error) {
	var res []Giveaway
	for rows.Next() {
		var g Giveaway
		if err := s.scanRow(rows, &g); err != nil {
			return nil, err
		}
		res = append(res, g)
	}
	return res, rows.Err()
}

type pgxRows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close()
}
