package store

import (
	"context"
	"time"
)

// UTMLink — UTM-ссылка на канал для трекинга источников подписок.
type UTMLink struct {
	ID          int64
	OwnerUserID int64
	ChannelID   int64
	Source      string // utm_source (название ссылки)
	Medium      string // utm_medium
	Campaign    string // utm_campaign
	UTMTerm     string // utm_term (ключевая фраза)
	UTMContent  string // utm_content (элемент: баннер/текст)
	GiveawayID  int64  // привязка к розыгрышу (retention после итогов)
	LinkType    string // unique (на канал) | super (на пост) | contest
	PostRef     string // ссылка/название поста-источника
	Code        string // код диплинка (без префикса utm_)
	Cost        float64 // стоимость выхода (₽), для CPA
	Clicks      int64
	Signups     int64
	Unsubs      int64
	Enabled     bool
}

// CreateUTMLink — генерация UTM-ссылки для отслеживания источников подписок.
// channelID=0 → NULL (легаси-ссылка без привязки к каналу).
// Возвращает ID созданной/обновлённой записи. code — БЕЗ префикса "utm_".
func (s *Store) CreateUTMLink(ctx context.Context, ownerUserID, channelID int64, source, medium, campaign, term, content, postRef, code string, cost float64, giveawayID int64, linkType string) (int64, error) {
	var id int64
	err := s.pool.QueryRow(ctx, `
		INSERT INTO utm_links (owner_user_id, channel_id, source, medium, campaign,
			utm_term, utm_content, post_ref, code, cost, giveaway_id, link_type)
		VALUES ($1, NULLIF($2, 0), $3, $4, $5, $6, $7, $8, $9, $10, NULLIF($11, 0), $12)
		ON CONFLICT (code) DO UPDATE
		  SET source = EXCLUDED.source,
		      medium = EXCLUDED.medium,
		      campaign = EXCLUDED.campaign,
		      utm_term = EXCLUDED.utm_term,
		      utm_content = EXCLUDED.utm_content,
		      post_ref = EXCLUDED.post_ref,
		      giveaway_id = COALESCE(EXCLUDED.giveaway_id, utm_links.giveaway_id),
		      link_type  = EXCLUDED.link_type
		RETURNING id
	`, ownerUserID, channelID, source, medium, campaign, term, content, postRef, code, cost, giveawayID, linkType).Scan(&id)
	return id, err
}

// GetUTMLinkByID — ссылка по внутреннему id (для очереди первичных проверок).
func (s *Store) GetUTMLinkByID(ctx context.Context, id int64) (UTMLink, error) {
	var u UTMLink
	err := s.pool.QueryRow(ctx, `
		SELECT id, COALESCE(owner_user_id,0), COALESCE(channel_id,0), source, medium, campaign,
		       utm_term, utm_content, COALESCE(giveaway_id,0), post_ref, code, cost, link_type,
		       clicks, signups, COALESCE(unsubs,0), COALESCE(enabled,true)
		FROM utm_links WHERE id=$1
	`, id).Scan(&u.ID, &u.OwnerUserID, &u.ChannelID, &u.Source, &u.Medium, &u.Campaign,
		&u.UTMTerm, &u.UTMContent, &u.GiveawayID, &u.PostRef, &u.Code, &u.Cost, &u.LinkType,
		&u.Clicks, &u.Signups, &u.Unsubs, &u.Enabled)
	return u, wrapNotFound(err)
}

// GetUTMLinkByCode — найти UTM-ссылку по коду диплинка (code без префикса).
func (s *Store) GetUTMLinkByCode(ctx context.Context, code string) (UTMLink, error) {
	var u UTMLink
	err := s.pool.QueryRow(ctx, `
		SELECT id, COALESCE(owner_user_id,0), COALESCE(channel_id,0), source, medium, campaign,
		       utm_term, utm_content, COALESCE(giveaway_id,0), post_ref, code, cost, link_type,
		       clicks, signups, COALESCE(unsubs,0), COALESCE(enabled,true)
		FROM utm_links WHERE code=$1
	`, code).Scan(&u.ID, &u.OwnerUserID, &u.ChannelID, &u.Source, &u.Medium, &u.Campaign,
		&u.UTMTerm, &u.UTMContent, &u.GiveawayID, &u.PostRef, &u.Code, &u.Cost, &u.LinkType,
		&u.Clicks, &u.Signups, &u.Unsubs, &u.Enabled)
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

// RecordAnonymousClick — клик по бесшовной ссылке (/r/<code>): HTTP-редирект
// не знает user_id (юзер не проходил через бот), поэтому пишем user_id=0.
// Клики инкрементятся в общей таблице — видны в той же аналитике.
func (s *Store) RecordAnonymousClick(ctx context.Context, utmLinkID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx) //nolint:errcheck
	if _, err := tx.Exec(ctx, `UPDATE utm_links SET clicks = clicks + 1 WHERE id=$1`, utmLinkID); err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `INSERT INTO utm_clicks (utm_link_id, user_id) VALUES ($1, 0)`, utmLinkID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// UTMAccountSignup — юзер вступил в канал (по CHAT_ID MAX); если есть
// атрибуция к UTM-ссылке на этот канал — инкремент signups.
// Возвращает UTM-ссылку, которой приписана подписка (или zero-value, если нет атрибуции).
//
// ВАЖНО: сравнение через JOIN channels по chat_id — utm_links.channel_id это
// ВНУТРЕННИЙ id канала (BIGSERIAL), а webhook приносит chat_id MAX (напр.
// -74970071115840). Раньше сравнивались напрямую → подписки/отписки по
// трекинг-ссылкам НИКОГДА не атрибутировались (клики считались, signups — нет).
func (s *Store) UTMAccountSignup(ctx context.Context, userID, chatID int64) (UTMLink, bool, error) {
	var u UTMLink
	err := s.pool.QueryRow(ctx, `
		SELECT ul.id, COALESCE(ul.owner_user_id,0), COALESCE(ul.channel_id,0), ul.source, ul.medium, ul.campaign, ul.post_ref, ul.code, ul.clicks, ul.signups, COALESCE(ul.enabled,true)
		FROM user_utm_attribution a
		JOIN utm_links ul ON ul.id = a.utm_link_id
		JOIN channels c ON c.id = ul.channel_id
		WHERE a.user_id=$1 AND c.chat_id=$2
	`, userID, chatID).Scan(&u.ID, &u.OwnerUserID, &u.ChannelID, &u.Source, &u.Medium, &u.Campaign, &u.PostRef, &u.Code, &u.Clicks, &u.Signups, &u.Enabled)
	if err != nil {
		return u, false, nil // нет атрибуции — органик
	}
	_, err = s.pool.Exec(ctx, `UPDATE utm_links SET signups = signups + 1 WHERE id=$1`, u.ID)
	return u, true, err
}

// UTMAccountUnsub — юзер отписался (по CHAT_ID MAX); инкремент unsubs если
// была атрибуция (см. комментарий UTMAccountSignup про chat_id).
func (s *Store) UTMAccountUnsub(ctx context.Context, userID, chatID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE utm_links SET unsubs = unsubs + 1
		WHERE id IN (
		  SELECT a.utm_link_id FROM user_utm_attribution a
		  JOIN utm_links ul ON ul.id = a.utm_link_id
		  JOIN channels c ON c.id = ul.channel_id
		  WHERE a.user_id=$1 AND c.chat_id=$2
		)
	`, userID, chatID)
	return err
}

// UTMStat — статистика одной UTM-ссылки (для отчёта).
type UTMStat struct {
	ID        int64
	Source    string
	Medium    string
	Campaign  string
	UTMTerm   string
	UTMContent string
	GiveawayID int64
	LinkType  string
	PostRef   string
	Code      string
	Cost      float64
	Clicks    int64
	Signups   int64
	Unsubs    int64
}

// ListUTMStats — все UTM-ссылки по каналу (с расширенной статистикой).
func (s *Store) ListUTMStats(ctx context.Context, ownerUserID, channelID int64) ([]UTMStat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, source, medium, campaign, utm_term, utm_content, COALESCE(giveaway_id,0),
		       post_ref, code, cost, link_type, clicks, signups, COALESCE(unsubs,0)
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
		if err := rows.Scan(&u.ID, &u.Source, &u.Medium, &u.Campaign, &u.UTMTerm, &u.UTMContent,
			&u.GiveawayID, &u.PostRef, &u.Code, &u.Cost, &u.LinkType, &u.Clicks, &u.Signups, &u.Unsubs); err != nil {
			return nil, err
		}
		res = append(res, u)
	}
	return res, rows.Err()
}

// ListUTMStatsAll — все UTM-ссылки владельца по всем каналам (сводный отчёт).
func (s *Store) ListUTMStatsAll(ctx context.Context, ownerUserID int64) ([]UTMStat, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, source, medium, campaign, utm_term, utm_content, COALESCE(giveaway_id,0),
		       post_ref, code, cost, link_type, clicks, signups, COALESCE(unsubs,0)
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
		if err := rows.Scan(&u.ID, &u.Source, &u.Medium, &u.Campaign, &u.UTMTerm, &u.UTMContent,
			&u.GiveawayID, &u.PostRef, &u.Code, &u.Cost, &u.LinkType, &u.Clicks, &u.Signups, &u.Unsubs); err != nil {
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

// ---------- Удержание подписчиков (retention, миграция 0014) ----------

// UpsertUTMSubscriber — юзер подписан по ссылке (событие user_added с
// атрибуцией). Повторный вызов не перезаписывает существующую запись.
func (s *Store) UpsertUTMSubscriber(ctx context.Context, utmLinkID, userID, chatID int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO utm_subscribers (utm_link_id, user_id, chat_id, subscribed_at, last_checked_at)
		VALUES ($1, $2, $3, now(), now())
		ON CONFLICT (utm_link_id, user_id) DO NOTHING
	`, utmLinkID, userID, chatID)
	return err
}

// MarkUTMUnsubscribedByUser — юзер отписался (событие user_removed): фиксируем
// отписку во всех свяженных с ним ссылках этого канала.
func (s *Store) MarkUTMUnsubscribedByUser(ctx context.Context, userID, chatID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE utm_subscribers us
		SET unsubscribed_at = COALESCE(us.unsubscribed_at, now())
		WHERE us.user_id = $1 AND us.chat_id = $2
	`, userID, chatID)
	return err
}

// UpsertUTMSubscriberAtClick — первичная проверка в момент клика: юзер уже
// УЧАСТНИК канала (user_added для него не придёт никогда). Фиксируем сразу
// отдельным флагом — метрика «уже были участниками на момент клика».
func (s *Store) UpsertUTMSubscriberAtClick(ctx context.Context, utmLinkID, userID, chatID int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO utm_subscribers (utm_link_id, user_id, chat_id, subscribed_at, last_checked_at, was_subscribed_at_click)
		VALUES ($1, $2, $3, now(), now(), TRUE)
		ON CONFLICT (utm_link_id, user_id) DO NOTHING
	`, utmLinkID, userID, chatID)
	return err
}

// UTMLinkRetention — удержание подписчиков одной ссылки: сколько подписались
// и сколько оставались на ключевых отметках (1/3/7/14 дней от подписки и,
// для привязанных к розыгрышу, — на момент его окончания).
type UTMLinkRetention struct {
	Subscribed    int // всего подписавшихся (обнаруженных)
	StillIn       int // подписаны сейчас
	WasMemberAtClick int // уже были участниками канала в момент клика
	Retained1d    int
	Retained3d    int
	Retained7d    int
	Retained14d   int
	Retained30d   int
	GiveawayEndAt *time.Time // время окончания привязанного розыгрыша (если закончился)
	RetainedAfterGiveaway int
}

// GetUTMLinkRetention — удержание по ссылке (окна: отписка позже отметки
// считается «держался»; суточная дискретность поллинга даёт ±1 день).
func (s *Store) GetUTMLinkRetention(ctx context.Context, utmLinkID int64) (UTMLinkRetention, error) {
	var r UTMLinkRetention
	err := s.pool.QueryRow(ctx, `
		SELECT
		  COUNT(*),
		  COUNT(*) FILTER (WHERE unsubscribed_at IS NULL),
		  COUNT(*) FILTER (WHERE was_subscribed_at_click),
		  COUNT(*) FILTER (WHERE unsubscribed_at IS NULL OR unsubscribed_at >= subscribed_at + interval '1 day'),
		  COUNT(*) FILTER (WHERE unsubscribed_at IS NULL OR unsubscribed_at >= subscribed_at + interval '3 days'),
		  COUNT(*) FILTER (WHERE unsubscribed_at IS NULL OR unsubscribed_at >= subscribed_at + interval '7 days'),
		  COUNT(*) FILTER (WHERE unsubscribed_at IS NULL OR unsubscribed_at >= subscribed_at + interval '14 days'),
		  COUNT(*) FILTER (WHERE unsubscribed_at IS NULL OR unsubscribed_at >= subscribed_at + interval '30 days')
		FROM utm_subscribers WHERE utm_link_id = $1
	`, utmLinkID).Scan(&r.Subscribed, &r.StillIn, &r.WasMemberAtClick, &r.Retained1d, &r.Retained3d, &r.Retained7d, &r.Retained14d, &r.Retained30d)
	if err != nil {
		return r, err
	}
	// Привязанный розыгрыш закончился → сколько оставалось на момент итогов.
	var end *time.Time
	if err := s.pool.QueryRow(ctx, `
		SELECT g.updated_at FROM utm_links ul
		JOIN giveaways g ON g.id = ul.giveaway_id
		WHERE ul.id = $1 AND g.status = 'finished'
	`, utmLinkID).Scan(&end); err == nil && end != nil {
		r.GiveawayEndAt = end
		_ = s.pool.QueryRow(ctx, `
			SELECT COUNT(*) FROM utm_subscribers
			WHERE utm_link_id = $1
			  AND (unsubscribed_at IS NULL OR unsubscribed_at >= $2)
		`, utmLinkID, *end).Scan(&r.RetainedAfterGiveaway)
	}
	return r, nil
}

// UTMSubscriberView — подписчик ссылки для мини-аппа.
type UTMSubscriberView struct {
	UserID        int64
	FirstName     string
	Username      string
	SubscribedAt  string
	UnsubscribedAt string // пусто = подписан
	WasMember     bool   // уже был участником на момент клика
}

// ListUTMSubscribers — подписчики ссылки с именами (миниапп).
func (s *Store) ListUTMSubscribers(ctx context.Context, utmLinkID int64) ([]UTMSubscriberView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT us.user_id, COALESCE(u.first_name,''), COALESCE(u.username,''),
		       to_char(us.subscribed_at AT TIME ZONE 'Europe/Moscow', 'DD.MM.YY HH24:MI'),
		       COALESCE(to_char(us.unsubscribed_at AT TIME ZONE 'Europe/Moscow', 'DD.MM.YY HH24:MI'), ''),
		       us.was_subscribed_at_click
		FROM utm_subscribers us
		JOIN users u ON u.user_id = us.user_id
		WHERE us.utm_link_id = $1
		ORDER BY us.subscribed_at DESC
		LIMIT 500
	`, utmLinkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []UTMSubscriberView
	for rows.Next() {
		var v UTMSubscriberView
		if err := rows.Scan(&v.UserID, &v.FirstName, &v.Username, &v.SubscribedAt, &v.UnsubscribedAt, &v.WasMember); err != nil {
			return nil, err
		}
		res = append(res, v)
	}
	return res, rows.Err()
}

// ListClickers — кликнувшие по ссылке (для списка юзеров в миниаппе):
// включая не подписавшихся.
type UTMClickerView struct {
	UserID    int64
	FirstName string
	Username  string
	ClickedAt string
	ClickedMSK bool
}

func (s *Store) ListUTMClickers(ctx context.Context, utmLinkID int64) ([]UTMClickerView, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.user_id, COALESCE(u.first_name,''), COALESCE(u.username,''),
		       to_char(c.started_at AT TIME ZONE 'Europe/Moscow', 'DD.MM.YY HH24:MI')
		FROM utm_clicks c
		JOIN users u ON u.user_id = c.user_id
		WHERE c.utm_link_id = $1 AND c.user_id <> 0
		ORDER BY c.started_at DESC
		LIMIT 500
	`, utmLinkID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []UTMClickerView
	for rows.Next() {
		var v UTMClickerView
		if err := rows.Scan(&v.UserID, &v.FirstName, &v.Username, &v.ClickedAt); err != nil {
			return nil, err
		}
		res = append(res, v)
	}
	return res, rows.Err()
}
