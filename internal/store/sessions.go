package store

import (
	"context"
	"encoding/json"

	"github.com/jackc/pgx/v5"
)

// Состояния юзера вне FSM розыгрыша (см. user_sessions в БД).
const (
	SessionNone          = ""                // нет активного состояния
	SessionAddingChannel = "adding_channel"  // ожидание ID канала / пересылки
	SessionCaptchaPct    = "captcha_pct"     // ожидание % капчи (FSM розыгрыша)
	SessionUTMCreate     = "utm_create"      // создание UTM-ссылки
	SessionCaptchaVerify = "captcha_verify"  // ожидание ответа капчи (при участии)
	SessionEditField     = "edit_field"      // редактура поля розыгрыша (scheduled/active)
)

// UserSession — произвольное состояние юзера с контекстом (payload).
type UserSession struct {
	State   string
	Payload map[string]any
}

// SetSession устанавливает состояние юзера (без payload).
func (s *Store) SetSession(ctx context.Context, userID int64, state string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_sessions (user_id, state) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET state = EXCLUDED.state, payload = '{}'::jsonb
	`, userID, state)
	return err
}

// SetSessionPayload устанавливает состояние + JSON-контекст.
func (s *Store) SetSessionPayload(ctx context.Context, userID int64, state string, payload map[string]any) error {
	payloadJSON, _ := jsonMarshal(payload)
	_, err := s.pool.Exec(ctx, `
		INSERT INTO user_sessions (user_id, state, payload) VALUES ($1, $2, $3)
		ON CONFLICT (user_id) DO UPDATE SET state = EXCLUDED.state, payload = EXCLUDED.payload
	`, userID, state, payloadJSON)
	return err
}

// GetSessionPayload возвращает состояние + payload (state=SessionNone если нет).
func (s *Store) GetSessionPayload(ctx context.Context, userID int64) (UserSession, error) {
	var (
		state    string
		payloadB []byte
	)
	err := s.pool.QueryRow(ctx, `SELECT state, payload FROM user_sessions WHERE user_id=$1`, userID).Scan(&state, &payloadB)
	if err != nil {
		if err == pgx.ErrNoRows {
			return UserSession{State: SessionNone}, nil
		}
		return UserSession{State: SessionNone}, err
	}
	sess := UserSession{State: state}
	if len(payloadB) > 0 {
		_ = jsonUnmarshal(payloadB, &sess.Payload)
	}
	if sess.Payload == nil {
		sess.Payload = map[string]any{}
	}
	return sess, nil
}

// GetSession возвращает текущее состояние юзера (SessionNone если нет).
func (s *Store) GetSession(ctx context.Context, userID int64) (string, error) {
	var state string
	err := s.pool.QueryRow(ctx, `SELECT state FROM user_sessions WHERE user_id=$1`, userID).Scan(&state)
	if err != nil {
		if err == pgx.ErrNoRows {
			return SessionNone, nil
		}
		return SessionNone, err
	}
	return state, nil
}

// ClearSession сбрасывает состояние юзера.
func (s *Store) ClearSession(ctx context.Context, userID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM user_sessions WHERE user_id=$1`, userID)
	return err
}

// ---------- json helpers (изолируем зависимость от encoding/json) ----------

func jsonMarshal(v any) ([]byte, error)  { return json.Marshal(v) }
func jsonUnmarshal(b []byte, v any) error { return json.Unmarshal(b, v) }
