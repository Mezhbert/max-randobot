package store

import "context"

// UTMLink — UTM-ссылка на канал для трекинга источников подписок.
type UTMLink struct {
	ID          int64
	OwnerUserID int64
	ChannelID   int64
	Source      string // utm_source
	Medium      string // utm_medium
	Campaign    string // utm_campaign
	PostRef     string // ссылка/название поста-источника
	Code        string // код диплинка (без префикса utm_)
	Clicks      int64
	Signups     int64
	Unsubs      int64
	Enabled     bool
}

// CreateUTMLink — генерация UTM-ссылки для отслеживания источника подписок на канал.
// Возвращает ID созданной/обновлённой записи. code — БЕЗ префикса "utm_".
func (s *Store) CreateUTMLink(ctx context.Context, ownerUserID, channelID int64, source, medium, campaign, postRef, code string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO utm_links (owner_user_id, channel_id, source, medium, campaign, post_ref, code)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (code) DO UPDATE
		  SET source = EXCLUDED.source,
		      medium = EXCLUDED.medium,
		      campaign = EXCLUDED.campaign,
		      post_ref = EXCLUDED.post_ref
		RETURNING id
	`, ownerUserID, channelID, source, medium, campaign, postRef, code).Scan(&id)
	return id, err
}

// GetUTMLinkByCode — найти UTM-ссылку по коду диплинка (code без префикса).
func (s *Store) GetUTMLinkByCode(ctx context.Context, code string) (UTMLink, error) {
	var u UTMLink
	err := s.pool.QueryRow(ctx, `
		SELECT id, COALESCE(owner_user_id,0), COALESCE(channel_id,0), source, medium, campaign, post_ref, code, clicks, signups, COALESCE(enabled,true)
		FROM utm_links WHERE code=$1
	`, code).Scan(&u.ID, &u.OwnerUserID, &u.ChannelID, &u.Source, &u.Medium, &u.Campaign, &u.PostRef, &u.Code, &u.Clicks, &u.Signups, &u.Enabled)
	return u, wrapNotFound(err)
}

// UTMRecordClick — фиксируем клик (старт бота по диплинку) + атрибуцию юзера.
func (s *Store) UTMRecordClick(ctx context.Context, utmLinkID, userID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `UPDATE utm_links SET clicks = clicks + 1 WHERE id=$1`, utmLinkID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO utm_clicks (utm_link_id, user_id) VALUES ($1, $2)`, utmLinkID, userID); err != nil {
		return err
	}
	// Атрибуция: последний источник выигрывает (upsert).
	if _, err := tx.Exec(ctx, `
		INSERT INTO user_utm_attribution (user_id, utm_link_id, attributed_at)
		VALUES ($1, $2, now())
		ON CONFLICT (user_id) DO UPDATE SET utm_link_id = EXCLUDED.utm_link_id, attributed_at = now()
	`, userID, utmLinkID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UTMAccountSignup — юзер вступил в канал; если есть атрибуция к UTM — инкремент signups.
// Возвращает UTM-ссылку, которой приписана подписка (или zero-value, если нет атрибуции).
func (s *Store) UTMAccountSignup(ctx context.Context, userID, channelID int64) (UTMLink, bool, error) {
	var u UTMLink
	// Ищем атрибуцию юзера, и проверяем что она ведёт на этот же канал.
	err := s.pool.QueryRow(ctx, `
		SELECT ul.id, COALESCE(ul.owner_user_id,0), COALESCE(ul.channel_id,0), ul.source, ul.medium, ul.campaign, ul.post_ref, ul.code, ul.clicks, ul.signups, COALESCE(ul.enabled,true)
		FROM user_utm_attribution a
		JOIN utm_links ul ON ul.id = a.utm_link_id
		WHERE a.user_id=$1 AND ul.channel_id=$2
	`, userID, channelID).Scan(&u.ID, &u.OwnerUserID, &u.ChannelID, &u.Source, &u.Medium, &u.Campaign, &u.PostRef, &u.Code, &u.Clicks, &u.Signups, &u.Enabled)
	if err != nil {
		return u, false, nil // нет атрибуции — органик
	}
	_, err = s.pool.Exec(ctx, `UPDATE utm_links SET signups = signups + 1 WHERE id=$1`, u.ID)
	return u, true, err
}

// UTMAccountUnsub — юзер отписался; инкремент unsubs если была атрибуция.
func (s *Store) UTMAccountUnsub(ctx context.Context, userID, channelID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE utm_links SET unsubs = unsubs + 1
		WHERE id IN (
		  SELECT a.utm_link_id FROM user_utm_attribution a
		  JOIN utm_links ul ON ul.id = a.utm_link_id
		  WHERE a.user_id=$1 AND ul.channel_id=$2
		)
	`, userID, channelID)
	return err
}

// UTMStat — статистика одной UTM-ссылки (для отчёта).
type UTMStat struct {
	ID       int64
	Source   string
	Medium   string
	Campaign string
	PostRef  string
	Code     string
	Clicks   int64
	Signups  int64
	Unsubs   int64
}

// ListUTMStats — все UTM-ссылки по каналу (с расширенной статистикой).
func (s *Store) ListUTMStats(ctx context.Context, ownerUserID, channelID int64) ([]UTMStat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, source, medium, campaign, post_ref, code, clicks, signups, COALESCE(unsubs,0)
		FROM utm_links
		WHERE owner_user_id=$1 AND channel_id=$2
		ORDER BY created_at DESC
	`, ownerUserID, channelID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []UTMStat
	for rows.Next() {
		var u UTMStat
		if err := rows.Scan(&u.ID, &u.Source, &u.Medium, &u.Campaign, &u.PostRef, &u.Code, &u.Clicks, &u.Signups, &u.Unsubs); err != nil {
			return nil, err
		}
		res = append(res, u)
	}
	return res, rows.Err()
}

// ListUTMStatsAll — все UTM-ссылки владельца по всем каналам (сводный отчёт).
func (s *Store) ListUTMStatsAll(ctx context.Context, ownerUserID int64) ([]UTMStat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, source, medium, campaign, post_ref, code, clicks, signups, COALESCE(unsubs,0)
		FROM utm_links
		WHERE owner_user_id=$1
		ORDER BY created_at DESC
	`, ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []UTMStat
	for rows.Next() {
		var u UTMStat
		if err := rows.Scan(&u.ID, &u.Source, &u.Medium, &u.Campaign, &u.PostRef, &u.Code, &u.Clicks, &u.Signups, &u.Unsubs); err != nil {
			return nil, err
		}
		res = append(res, u)
	}
	return res, rows.Err()
}

// UTMChannelSummary — сводная статистика по одному каналу.
type UTMChannelSummary struct {
	ChannelID int64
	Title     string
	Links     int
	Clicks    int64
	Signups   int64
	Unsubs    int64
}

// ListUTMChannelSummaries — сводка по всем каналам владельца с UTM-ссылками.
func (s *Store) ListUTMChannelSummaries(ctx context.Context, ownerUserID int64) ([]UTMChannelSummary, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, COALESCE(c.title, 'канал #' || c.chat_id),
		       COUNT(u.id), COALESCE(SUM(u.clicks),0), COALESCE(SUM(u.signups),0), COALESCE(SUM(u.unsubs),0)
		FROM channels c
		LEFT JOIN utm_links u ON u.channel_id = c.id AND u.owner_user_id = $1
		WHERE c.owner_user_id = $1
		GROUP BY c.id, c.title, c.chat_id
		ORDER BY c.created_at
	`, ownerUserID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []UTMChannelSummary
	for rows.Next() {
		var sum UTMChannelSummary
		if err := rows.Scan(&sum.ChannelID, &sum.Title, &sum.Links, &sum.Clicks, &sum.Signups, &sum.Unsubs); err != nil {
			return nil, err
		}
		res = append(res, sum)
	}
	return res, rows.Err()
}

// EnsureReferralLink — создаёт реферальную ссылку участника (или возвращает существующий код).
func (s *Store) EnsureReferralLink(ctx context.Context, giveawayID, userID int64, code string) (string, error) {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO referral_links (giveaway_id, user_id, code)
		VALUES ($1, $2, $3)
		ON CONFLICT (giveaway_id, user_id) DO NOTHING
	`, giveawayID, userID, code)
	return code, err
}
