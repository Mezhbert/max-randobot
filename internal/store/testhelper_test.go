package store

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
	tc "github.com/testcontainers/testcontainers-go"
	tcwait "github.com/testcontainers/testcontainers-go/wait"
)

// newTestStore поднимает свежий PostgreSQL в Docker (testcontainers), применяет
// все миграции и возвращает Store + функцию очистки. Каждая функция TestMain /
// каждого пакета — один контейнер; отдельные тесты работают с одним Store,
// но используют уникальные данные, чтобы не мешать друг другу.
//
// Тесты с префиксом integ_ требуют Docker; без Docker они пропускаются.
func newTestStore(t *testing.T) (*Store, func()) {
	t.Helper()
	ctx := context.Background()

	if testing.Short() {
		t.Skip("пропуск интеграционного теста в -short режиме")
	}

	req := tc.ContainerRequest{
		Image:        "postgres:16-alpine",
		ExposedPorts: []string{"5432/tcp"},
		Env: map[string]string{
			"POSTGRES_USER":     "test",
			"POSTGRES_PASSWORD": "test",
			"POSTGRES_DB":       "test",
		},
		WaitingFor: tcwait.ForLog("database system is ready to accept connections").
			WithOccurrence(2).WithStartupTimeout(40 * time.Second),
	}
	container, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		t.Skipf("Docker недоступен — пропуск интеграционного теста: %v", err)
	}
	cleanup := func() {
		_ = container.Terminate(context.Background())
	}

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "5432")
	require.NoError(t, err)

	dsn := fmt.Sprintf("postgres://test:test@%s:%s/test?sslmode=disable", host, port.Port())
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)

	require.NoError(t, Migrate(ctx, pool))

	return New(pool), func() {
		pool.Close()
		cleanup()
	}
}

// freshGiveaway — создание giveaway в заданном статусе для тестов scheduler/claim.
func (s *Store) freshGiveaway(ctx context.Context, t *testing.T, owner int64, status string) Giveaway {
	t.Helper()
	// Гарантируем существование owner в users (FK constraint giveaways_owner_user_id_fkey).
	_, err := s.pool.Exec(ctx, `
		INSERT INTO users (user_id) VALUES ($1)
		ON CONFLICT (user_id) DO NOTHING
	`, owner)
	require.NoError(t, err)
	// Создаём через draft, потом переводим в нужный статус напрямую.
	g, err := s.CreateDraftGiveaway(ctx, owner, "post_text")
	require.NoError(t, err)
	require.NotZero(t, g.ID)
	if status != StatusDraft {
		_, err = s.pool.Exec(ctx,
			`UPDATE giveaways SET status=$2::giveaway_status WHERE id=$1`, g.ID, status)
		require.NoError(t, err)
	}
	return g
}
