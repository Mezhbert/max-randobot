package store

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
)

// Тарифы создателей розыгрышей. Значения совпадают со значениями в БД
// (feature_limits.tier, owners.tier). Не строковые литералы в коде —
// единственный источник правды здесь.
const (
	TierFree     = "free"
	TierPro      = "pro"
	TierBusiness = "business"
)

// Ключи функционала для feature_limits. Совпадают со значениями в миграции 0003.
const (
	FeatureMaxChannels = "max_channels"
	FeatureUTMLinks    = "utm_links"
	FeatureReferral    = "referral"
	FeaturePromotion   = "promotion"
)

// FeatureOK сообщает, разрешён ли функционал на тарифе.
// limit_value == -1 (безлимит) или > 0 — разрешено; == 0 — выключено.
func FeatureOK(limit int) bool { return limit != 0 }

// Owner — создатель розыгрышей (отдельно от участника-юзера).
type Owner struct {
	UserID        int64
	Tier          string
	TierExpiresAt *time.Time // nullable
}

// EnsureOwner создаёт запись владельца, если её ещё нет (дефолт — free).
// Идемпотентно. Используется при первом действии "создателя" (создание
// розыгрыша, добавление канала, UTM-ссылка).
func (s *Store) EnsureOwner(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO owners (user_id) VALUES ($1)
		ON CONFLICT (user_id) DO NOTHING
	`, userID)
	return err
}

// GetOwnerTier возвращает текущий тариф создателя. Если записи нет — free.
func (s *Store) GetOwnerTier(ctx context.Context, userID int64) (string, error) {
	var tier string
	err := s.pool.QueryRow(ctx, `
		SELECT tier FROM owners WHERE user_id=$1
	`, userID).Scan(&tier)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return TierFree, nil
		}
		return TierFree, err
	}
	return tier, nil
}

// SetOwnerTier выставляет тариф создателя (и срок истечения, опционально).
// tierExpiresAt == nil означает "бессрочно или free".
func (s *Store) SetOwnerTier(ctx context.Context, userID int64, tier string, tierExpiresAt *time.Time) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO owners (user_id, tier, tier_expires_at)
		VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE
		  SET tier = EXCLUDED.tier,
		      tier_expires_at = EXCLUDED.tier_expires_at
	`, userID, tier, tierExpiresAt)
	return err
}

// FeatureLimit возвращает лимит функционала для тарифа.
// limit_value: -1 = безлимит, 0 = выключен, >0 = числовой лимит.
// Если тариф/фича не найдены — возвращает 0 (дефолт "выключено") без ошибки,
// чтобы не блокировать работу при пустом справочнике.
func (s *Store) FeatureLimit(ctx context.Context, tier, featureKey string) (int, error) {
	var limit int
	err := s.pool.QueryRow(ctx, `
		SELECT limit_value FROM feature_limits WHERE tier=$1 AND feature_key=$2
	`, tier, featureKey).Scan(&limit)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, nil
		}
		return 0, err
	}
	return limit, nil
}

// HasFeature — shorthand: тариф создателя + фича разрешена?
func (s *Store) HasFeature(ctx context.Context, userID int64, featureKey string) (bool, error) {
	tier, err := s.GetOwnerTier(ctx, userID)
	if err != nil {
		return false, err
	}
	limit, err := s.FeatureLimit(ctx, tier, featureKey)
	if err != nil {
		return false, err
	}
	return FeatureOK(limit), nil
}
