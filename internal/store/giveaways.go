package store

import (
	"context"
	"encoding/json"
	"strconv"
	"time"
)

// CreateDraftGiveaway создаёт новый черновик розыгрыша для пользователя.
// Каждый «Создать розыгрыш» = новый черновик. Старые продолжаются отдельно
// через ResumeDraft (кнопка «Продолжить создание» в списке черновиков).
func (s *Store) CreateDraftGiveaway(ctx context.Context, ownerUserID int64, step string) (Giveaway, error) {
	var g Giveaway
	err := s.pool.QueryRow(ctx, `
		INSERT INTO giveaways (owner_user_id, draft_step, status)
		VALUES ($1, $2, 'draft'::giveaway_status)
		RETURNING `+giveawayCols+`
	`, ownerUserID, step,
	).Scan(scanDest(&g)...)
	if err != nil {
		return g, err
	}
	g.finalizeScan() // конвертируем сырые pgtype-поля (draftStep и т.д.) в типизированные
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

// fieldWhitelist — разрешённые к точечному обновлению колонки giveaways.
// Ровно те, что реально пишутся FSM/редактурой. Мёртвые legacy-колонки
// (referral_*, promotion_*, captcha_percent, post_format) убраны: разрешать
// больше, чем используется — лишняя поверхность для mass assignment.
var fieldWhitelist = map[string]bool{
	"name": true, "post_text": true, "button_text": true,
	"media_tokens": true,
	"subscribe_channel_ids": true, "publish_channel_ids": true,
	"results_in_original_post": true, "results_post_text": true,
	"publish_at": true, "finish_at": true, "finish_mode": true, "finish_participants_count": true,
	"winners_count": true, "winners_message": true,
	"captcha_mode": true,
	"draft_step": true, "channel_select_mid": true, "publish_now": true,
}

// UpdateGiveawayField — точечное обновление одного поля черновика в процессе FSM.
// field — имя колонки, val — значение. Безопасно: field из белого списка FSM.
// Только для status='draft' (для scheduled/active — UpdateGiveawayFieldAny).
func (s *Store) UpdateGiveawayField(ctx context.Context, id int64, field string, val any) error {
	if !fieldWhitelist[field] {
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

// UpdateGiveawayFieldAny — точечное обновление поля для ЛЮБОГО статуса
// (редактура scheduled/active розыгрышей). Тот же белый список полей.
// Не трогает finished/cancelled логику — это ответственность вызывающего.
func (s *Store) UpdateGiveawayFieldAny(ctx context.Context, id int64, field string, val any) error {
	if !fieldWhitelist[field] {
		return ErrNotFound
	}
	tag, err := s.pool.Exec(ctx,
		`UPDATE giveaways SET `+field+`=$2, updated_at=now() WHERE id=$1`, id, val)
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

// AbandonDraft — пользователь вышел из создания (Отменить/В меню): черновик
// сохраняется со статусом draft и ТЕКУЩИМ draft_step (НЕ сбрасывается!).
// Так «Продолжить создание» начнёт с того шага, где остановились, а не с начала.
func (s *Store) AbandonDraft(ctx context.Context, id int64) error {
	// Ничего не меняем — просто оставляем черновик как есть.
	// draft_step сохраняется, чтобы ResumeDraft продолжил с него.
	_, err := s.pool.Exec(ctx,
		`UPDATE giveaways SET updated_at=now() WHERE id=$1 AND status='draft'`, id)
	return err
}

// DeleteEmptyDrafts удаляет черновики без названия (мусор от прерванных попыток
// создания). Именованные черновики (сохранённые) не трогает.
func (s *Store) DeleteEmptyDrafts(ctx context.Context, ownerUserID int64) error {
	_, err := s.pool.Exec(ctx,
		`DELETE FROM giveaways WHERE owner_user_id=$1 AND status='draft' AND (name IS NULL OR name='')`, ownerUserID)
	return err
}

// PublishDraft — подтверждение создания: статус всегда → scheduled.
// Scheduler сам опубликует пост по наступлении publish_at (даже если оно
// «прямо сейчас» — следующий тик подхватит). Это гарантирует, что статус active
// появляется ТОЛЬКО после реальной публикации поста в канал, а не «мимо» —
// иначе бот мог бы подвести итоги по неопубликованному розыгрышу.
func (s *Store) PublishDraft(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE giveaways
		SET status = 'scheduled'::giveaway_status,
		    draft_step = NULL
		WHERE id=$1 AND status='draft'
	`, id)
	return err
}

// SetStatus — смена статуса (внутренние переходы: publishing→active и т.п.).
// Явный cast: колонка status имеет тип giveaway_status (enum).
func (s *Store) SetStatus(ctx context.Context, id int64, status string) error {
	_, err := s.pool.Exec(ctx, `UPDATE giveaways SET status=$2::giveaway_status WHERE id=$1`, id, status)
	return err
}

// CancelGiveaway — отмена розыгрыша ПОЛЬЗОВАТЕЛЕМ. Владелец и допустимый
// статус проверяются атомарно в SQL: отменить завершённый/уже отменённый
// (crafted-callback или гонка с планировщиком) невозможно.
func (s *Store) CancelGiveaway(ctx context.Context, id, ownerUserID int64) error {
	tag, err := s.pool.Exec(ctx, `
		UPDATE giveaways SET status='cancelled'::giveaway_status, updated_at=now()
		WHERE id=$1 AND owner_user_id=$2 AND status IN ('scheduled','active')
	`, id, ownerUserID)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
}

// ---------------------------------------------------------------------------
// Atomic claim — захват задачи планировщиком.
//
// Проблема: неатомарная цепочка publish/finish (PickWinners → SendText × N →
// публикация итогов → смена статуса) успевала переотправить всё при следующем
// тике, пока статус не сменился. Решение — перевод в промежуточный статус
// 'publishing'/'finishing' атомарным UPDATE...RETURNING: выигрывает один вызов,
// остальные получают ErrNotFound и пропускают. На успехе — переход в
// 'active'/'finished'; на ошибке — Revert* обратно к предыдущему статусу.
// Застрявшие после краша процесса (status stuck in 'publishing'/'finishing')
// подбираются ListStaleClaimed.
// ---------------------------------------------------------------------------

// claimFrom атомарно переводит розыгрыш из fromStatus в toStatus.
// Возвращает захваченный розыгрыш или ErrNotFound, если статус уже не from
// (значит кто-то другой — или этот же процесс в прошлом тике — уже захватил).
func (s *Store) claimFrom(ctx context.Context, id int64, fromStatus, toStatus string) (Giveaway, error) {
	var g Giveaway
	err := s.pool.QueryRow(ctx, `
		UPDATE giveaways SET status=$3::giveaway_status
		WHERE id=$1 AND status=$2::giveaway_status
		RETURNING `+giveawayCols+`
	`, id, fromStatus, toStatus).Scan(scanDest(&g)...)
	if err != nil {
		return g, wrapNotFound(err)
	}
	g.finalizeScan()
	return g, nil
}

// ClaimForPublish переводит scheduled-розыгрыш в 'publishing' (захват).
// Возвращает ErrNotFound, если статус уже не scheduled.
func (s *Store) ClaimForPublish(ctx context.Context, id int64) (Giveaway, error) {
	return s.claimFrom(ctx, id, StatusScheduled, StatusPublishing)
}

// ClaimForFinish переводит active-розыгрыш (finish_at<=now) в 'finishing'.
func (s *Store) ClaimForFinish(ctx context.Context, id int64) (Giveaway, error) {
	return s.claimFrom(ctx, id, StatusActive, StatusFinishing)
}

// RevertPublish откатывает публикацию обратно в scheduled (ошибка/нет каналов).
func (s *Store) RevertPublish(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE giveaways SET status='scheduled'::giveaway_status WHERE id=$1 AND status='publishing'`, id)
	return err
}

// RevertFinish откатывает подведение итогов обратно в active.
func (s *Store) RevertFinish(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE giveaways SET status='active'::giveaway_status WHERE id=$1 AND status='finishing'`, id)
	return err
}

// MarkPublished завершает публикацию: publishing → active.
func (s *Store) MarkPublished(ctx context.Context, id int64) error {
	return s.SetStatus(ctx, id, StatusActive)
}

// MarkFinished завершает розыгрыш и помечает итоги опубликованными.
func (s *Store) MarkFinished(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx,
		`UPDATE giveaways SET status='finished'::giveaway_status, results_published=TRUE
		 WHERE id=$1 AND status='finishing'`, id)
	return err
}

// ListStaleClaimed возвращает розыгрыши, застрявшие в 'publishing'/'finishing'
// дольше olderThan (например, после краха процесса). Scheduler переоткатывает их
// (Revert*), чтобы они снова попали в очередь. Ограничиваем batchSize, чтобы
// один проход не перегрузил.
func (s *Store) ListStaleClaimed(ctx context.Context, olderThan time.Time, batchSize int) ([]Giveaway, error) {
	if batchSize <= 0 {
		batchSize = 50
	}
	rows, err := s.pool.Query(ctx, `
		SELECT `+giveawayCols+` FROM giveaways
		WHERE status IN ('publishing','finishing') AND updated_at < $1
		ORDER BY updated_at
		LIMIT $2
	`, olderThan, batchSize)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.scanGiveaways(rows)
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

// ActiveFinished — текущие розыгрыши, готовые к завершению:
//   - finish_mode='time': наступило finish_at;
//   - finish_mode='count': число участников достигло finish_participants_count.
func (s *Store) ActiveFinished(ctx context.Context, now time.Time) ([]Giveaway, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT `+giveawayCols+` FROM giveaways g
		WHERE status='active' AND (
		    (COALESCE(finish_mode,'time')='time' AND finish_at IS NOT NULL AND finish_at<=$1)
		    OR
		    (finish_mode='count' AND finish_participants_count > 0 AND
		     finish_participants_count <= (SELECT COUNT(*) FROM participants p WHERE p.giveaway_id = g.id))
		)
		ORDER BY finish_at NULLS LAST
	`, now)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return s.scanGiveaways(rows)
}

// StorePublishMessageID сохраняет mid опубликованного сообщения в канале.
// Ключ — chat_id (как текст), значение — mid. chatID конвертируем в строку
// в Go: pgx не умеет кодировать int64 → text напрямую (OID 25).
func (s *Store) StorePublishMessageID(ctx context.Context, id, chatID int64, mid string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE giveaways
		SET publish_message_ids = publish_message_ids || jsonb_build_object($2::text, $3::text)
		WHERE id=$1
	`, id, strconv.FormatInt(chatID, 10), mid)
	return err
}

// StoreResultsMessageID — mid поста с итогами (тот же паттерн, что StorePublishMessageID).
func (s *Store) StoreResultsMessageID(ctx context.Context, id, chatID int64, mid string) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE giveaways
		SET results_message_ids = results_message_ids || jsonb_build_object($2::text, $3::text)
		WHERE id=$1
	`, id, strconv.FormatInt(chatID, 10), mid)
	return err
}

// SaveWinners — фиксируем победителей.
// SaveWinners сохраняет список ID победителей. Колонка winners_user_ids —
// BIGINT[] NOT NULL, поэтому пустой slice нельзя передавать как NULL: при
// отсутствии участников явно пишем пустой массив '{}'.
func (s *Store) SaveWinners(ctx context.Context, id int64, winnerIDs []int64) error {
	if len(winnerIDs) == 0 {
		// Явный пустой массив — избегаем NULL для NOT NULL-колонки.
		_, err := s.pool.Exec(ctx, `UPDATE giveaways SET winners_user_ids='{}' WHERE id=$1`, id)
		return err
	}
	_, err := s.pool.Exec(ctx, `UPDATE giveaways SET winners_user_ids=$2 WHERE id=$1`, id, winnerIDs)
	return err
}

// MarkWinnersNotified фиксирует, кому из победителей уже отправлено ЛС
// (идемпотентность: после краша не слать повторно). Добавляет к существующему
// массиву, чтобы повторный частичный прогон добил недополучивших.
func (s *Store) MarkWinnersNotified(ctx context.Context, id int64, userIDs []int64) error {
	if len(userIDs) == 0 {
		return nil
	}
	_, err := s.pool.Exec(ctx,
		`UPDATE giveaways SET winners_notified_user_ids =
		    ARRAY(SELECT DISTINCT unnest(winners_notified_user_ids || $2::bigint[]))
		 WHERE id=$1`, id, userIDs)
	return err
}

// SetResultsPublished помечает итоги опубликованными (для идемпотентности
// повторного прогона finish после краша между публикацией и сменой статуса).
func (s *Store) SetResultsPublished(ctx context.Context, id int64) error {
	_, err := s.pool.Exec(ctx, `UPDATE giveaways SET results_published=TRUE WHERE id=$1`, id)
	return err
}

// ---------- сканеры ----------

const giveawayCols = `id, owner_user_id, name, post_text, post_format, media_tokens, button_text,
	subscribe_channel_ids, publish_channel_ids, results_in_original_post, results_post_text,
	publish_at, finish_at, finish_mode, finish_participants_count, winners_count, winners_message,
	referral_enabled, referral_limit, captcha_mode, captcha_percent, promotion_enabled,
	draft_step, status, publish_message_ids, results_message_ids, winners_user_ids,
	winners_notified_user_ids, results_published, channel_select_mid, publish_now, created_at, updated_at`

// giveawayColNames — имена колонок в том же порядке, что giveawayCols.
// Используется giveawayColsAlias для подстановки алиаса в JOIN'ах.
var giveawayColNames = []string{
	"id", "owner_user_id", "name", "post_text", "post_format", "media_tokens", "button_text",
	"subscribe_channel_ids", "publish_channel_ids", "results_in_original_post", "results_post_text",
	"publish_at", "finish_at", "finish_mode", "finish_participants_count", "winners_count", "winners_message",
	"referral_enabled", "referral_limit", "captcha_mode", "captcha_percent", "promotion_enabled",
	"draft_step", "status", "publish_message_ids", "results_message_ids", "winners_user_ids",
	"winners_notified_user_ids", "results_published", "channel_select_mid", "publish_now", "created_at", "updated_at",
}

func giveawayColsAlias(alias string) string {
	out := ""
	for i, c := range giveawayColNames {
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
	if err := row.Scan(scanDest(g)...); err != nil {
		return wrapNotFound(err)
	}
	g.finalizeScan()
	return nil
}

type rowIter interface {
	Scan(dest ...any) error
	Next() bool
	Err() error
}

func (s *Store) scanGiveawayRow(rows rowIter, g *Giveaway) error {
	if err := rows.Scan(scanDest(g)...); err != nil {
		return err
	}
	g.finalizeScan()
	return nil
}

// scanDest возвращает slice приёмников под giveawayCols — единое место, чтобы
// порядок колонок и порядок приёмников не разошёлся (был дубликат в 3 местах).
func scanDest(g *Giveaway) []any {
	return []any{
		&g.ID, &g.OwnerUserID, &g.Name, &g.PostText, &g.PostFormat, &g.mediaTokensRaw, &g.ButtonText,
		&g.SubscribeChannelIDs, &g.PublishChannelIDs, &g.ResultsInOriginalPost, &g.ResultsPostText,
		&g.publishAt, &g.finishAt, &g.FinishMode, &g.FinishParticipantsCount, &g.WinnersCount, &g.WinnersMessage,
		&g.ReferralEnabled, &g.ReferralLimit, &g.CaptchaMode, &g.CaptchaPercent, &g.PromotionEnabled,
		&g.draftStep, &g.Status, &g.publishMsgs, &g.resultsMsgs, &g.WinnersUserIDs,
		&g.WinnersNotifiedUserIDs, &g.ResultsPublished, &g.channelSelectMID, &g.PublishNow, &g.CreatedAt, &g.UpdatedAt,
	}
}

// finalizeScan расшифровывает «сырые» поля (JSONB, timestamptz) в типизированные.
// Должен вызываться после Scan. Вынесен отдельно, т.к. scanDest хранит сырое.
func (g *Giveaway) finalizeScan() {
	if g.draftStep.Valid {
		g.DraftStep = g.draftStep.String
	}
	if g.channelSelectMID.Valid {
		g.ChannelSelectMID = g.channelSelectMID.String
	}
	if g.publishAt.Valid {
		g.PublishAt = &g.publishAt.Time
	}
	if g.finishAt.Valid {
		g.FinishAt = &g.finishAt.Time
	}
	if g.MediaTokens == nil {
		g.MediaTokens = []MediaToken{}
	}
	_ = json.Unmarshal(g.mediaTokensRaw, &g.MediaTokens)
	if g.PublishMessageIDs == nil {
		g.PublishMessageIDs = map[int64]string{}
	}
	_ = json.Unmarshal(g.publishMsgs, &g.PublishMessageIDs)
	if g.ResultsMessageIDs == nil {
		g.ResultsMessageIDs = map[int64]string{}
	}
	_ = json.Unmarshal(g.resultsMsgs, &g.ResultsMessageIDs)
	if g.SubscribeChannelIDs == nil {
		g.SubscribeChannelIDs = []int64{}
	}
	if g.PublishChannelIDs == nil {
		g.PublishChannelIDs = []int64{}
	}
	if g.WinnersUserIDs == nil {
		g.WinnersUserIDs = []int64{}
	}
	if g.WinnersNotifiedUserIDs == nil {
		g.WinnersNotifiedUserIDs = []int64{}
	}
}

func (s *Store) scanGiveaways(rows pgxRows) ([]Giveaway, error) {
	var res []Giveaway
	for rows.Next() {
		var g Giveaway
		if err := rows.Scan(scanDest(&g)...); err != nil {
			return nil, err
		}
		g.finalizeScan()
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
