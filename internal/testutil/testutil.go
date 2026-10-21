// Package testutil — общие тестовые утилиты: mock BotAPI и фабрика test-PG.
// Используется сценарными тестами в scheduler/handlers.
package testutil

import (
	"context"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/maxbot/giveawaybot/internal/maxapi"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/stretchr/testify/require"
	tc "github.com/testcontainers/testcontainers-go"
	tcwait "github.com/testcontainers/testcontainers-go/wait"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// NewTestStore поднимает свежий PostgreSQL (testcontainers) + миграции.
func NewTestStore(t *testing.T) (*store.Store, func()) {
	t.Helper()
	if testing.Short() {
		t.Skip("пропуск интеграционного теста в -short режиме")
	}
	ctx := context.Background()

	container, err := tc.GenericContainer(ctx, tc.GenericContainerRequest{
		ContainerRequest: tc.ContainerRequest{
			Image:        "postgres:16-alpine",
			ExposedPorts: []string{"5432/tcp"},
			Env: map[string]string{
				"POSTGRES_USER":     "test",
				"POSTGRES_PASSWORD": "test",
				"POSTGRES_DB":       "test",
			},
			WaitingFor: tcwait.ForLog("database system is ready to accept connections").
				WithOccurrence(2).WithStartupTimeout(40 * time.Second),
		},
		Started: true,
	})
	if err != nil {
		t.Skipf("Docker недоступен — пропуск интеграционного теста: %v", err)
	}
	host, _ := container.Host(ctx)
	port, _ := container.MappedPort(ctx, "5432")

	dsn := fmt.Sprintf("postgres://test:test@%s:%s/test?sslmode=disable", host, port.Port())
	pool, err := pgxpool.New(ctx, dsn)
	require.NoError(t, err)
	require.NoError(t, store.Migrate(ctx, pool))

	return store.New(pool), func() {
		pool.Close()
		_ = container.Terminate(context.Background())
	}
}

// EnsureUser гарантирует существование пользователя в users (FK).
func EnsureUser(t *testing.T, s *store.Store, userID int64, username string) {
	t.Helper()
	_, err := s.Pool().Exec(context.Background(), `
		INSERT INTO users (user_id, username) VALUES ($1, $2)
		ON CONFLICT (user_id) DO UPDATE SET username = EXCLUDED.username
	`, userID, username)
	require.NoError(t, err)
}

// ---------- MockBotAPI — реализация maxapi.BotAPI для тестов ----------

// SendRecord — зафиксированный вызов отправки сообщения.
type SendRecord struct {
	Method   string // SendText | SendTextWithKeyboard | SendToChat | SendWithAttachments
	UserID   int64
	ChatID   int64
	Text     string
	Format   model.TextFormat
	HasKb    bool
	Buttons  []*model.Button // кнопки inline-клавиатуры (плоско; nil — без клавиатуры)
}

// EditRecord — зафиксированный вызов EditChatText.
type EditRecord struct {
	MessageID string
	Text      string
	Format    model.TextFormat
}

// MockBotAPI — записывает все вызовы к MAX API и позволяет настраивать ответы.
type MockBotAPI struct {
	mu      sync.Mutex
	counter int64

	Sends     []SendRecord
	Edits     []EditRecord
	Callbacks []string // answers callback ids

	// Ответы (настраиваются тестом).
	SendToChatMID   func(chatID int64) (string, error) // по умолчанию возвращает уникальный mid
	EditErr         func(mid string) error             // по умолчанию nil
	SendTextErr     error                              // ошибка SendText* (по умолчанию nil)
	IsSubscriberMap map[int64]map[int64]bool           // chatID -> userID -> subscribed
	IsBotAdminMap   map[int64]bool                     // chatID -> is admin (нет записи -> true)
}

// NewMock создаёт MockBotAPI с дефолтными успешными ответами.
func NewMock() *MockBotAPI {
	var counter int64
	return &MockBotAPI{
		SendToChatMID: func(chatID int64) (string, error) {
			counter++
			return fmt.Sprintf("mid.%d.%d", chatID, counter), nil
		},
		IsSubscriberMap: map[int64]map[int64]bool{},
	}
}

func (m *MockBotAPI) SendText(ctx context.Context, userID int64, text string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Sends = append(m.Sends, SendRecord{Method: "SendText", UserID: userID, Text: text})
	return m.SendTextErr
}

func (m *MockBotAPI) SendTextWithKeyboard(ctx context.Context, userID int64, text string, kb *model.Keyboard) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rec := SendRecord{Method: "SendTextWithKeyboard", UserID: userID, Text: text, HasKb: kb != nil}
	if kb != nil {
		for _, row := range kb.Build().Payload.Buttons {
			rec.Buttons = append(rec.Buttons, row...)
		}
	}
	m.Sends = append(m.Sends, rec)
	if m.SendTextErr != nil {
		return "", m.SendTextErr
	}
	m.counter++
	return fmt.Sprintf("mid.kb.%d", m.counter), m.SendTextErr
}

func (m *MockBotAPI) SendWithAttachments(ctx context.Context, userID int64, text string, attachments []model.Attachment, format model.TextFormat) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Sends = append(m.Sends, SendRecord{Method: "SendWithAttachments", UserID: userID, Text: text, Format: format})
	return m.SendTextErr
}

func (m *MockBotAPI) SendToChat(ctx context.Context, chatID int64, text string, attachments []model.Attachment, format model.TextFormat) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Sends = append(m.Sends, SendRecord{Method: "SendToChat", ChatID: chatID, Text: text, Format: format})
	if m.SendToChatMID != nil {
		return m.SendToChatMID(chatID)
	}
	return "mid.test", nil
}

func (m *MockBotAPI) EditChatText(ctx context.Context, messageID, text string, attachments []model.Attachment, format model.TextFormat) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Edits = append(m.Edits, EditRecord{MessageID: messageID, Text: text, Format: format})
	if m.EditErr != nil {
		return m.EditErr(messageID)
	}
	return nil
}

func (m *MockBotAPI) IsSubscriber(ctx context.Context, chatID, userID int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if subs, ok := m.IsSubscriberMap[chatID]; ok {
		return subs[userID], nil
	}
	return false, nil
}

func (m *MockBotAPI) IsBotAdminInChat(ctx context.Context, chatID int64) (bool, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if v, ok := m.IsBotAdminMap[chatID]; ok {
		return v, nil
	}
	return true, nil
}

func (m *MockBotAPI) CheckChannelAdmin(ctx context.Context, chatID int64) (model.Chat, bool, error) {
	return model.Chat{ChatID: chatID, Title: fmt.Sprintf("канал #%d", chatID)}, true, nil
}

func (m *MockBotAPI) GetChatInfo(ctx context.Context, chatID int64) (model.Chat, error) {
	return model.Chat{ChatID: chatID, Title: fmt.Sprintf("канал #%d", chatID)}, nil
}

func (m *MockBotAPI) GetChatByLink(ctx context.Context, link string) (model.Chat, error) {
	return model.Chat{ChatID: 424242, Title: "канал по ссылке", Link: link}, nil
}

func (m *MockBotAPI) AnswerCallback(ctx context.Context, callbackID, notification string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.Callbacks = append(m.Callbacks, callbackID)
}

func (m *MockBotAPI) SendImage(ctx context.Context, userID int64, pngData []byte, caption string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counter++
	m.Sends = append(m.Sends, SendRecord{Method: "SendImage", UserID: userID, Text: caption})
	return m.SendTextErr
}

func (m *MockBotAPI) PinChatMessage(ctx context.Context, chatID int64, messageID string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counter++
	m.Sends = append(m.Sends, SendRecord{Method: "PinChatMessage", ChatID: chatID, Text: messageID})
	return m.SendTextErr
}

func (m *MockBotAPI) SendFile(ctx context.Context, userID int64, filename string, data []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.counter++
	m.Sends = append(m.Sends, SendRecord{Method: "SendFile", UserID: userID, Text: filename})
	return m.SendTextErr
}

// Снимки (thread-safe).

func (m *MockBotAPI) SnapshotSends() []SendRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SendRecord, len(m.Sends))
	copy(out, m.Sends)
	return out
}

func (m *MockBotAPI) SnapshotEdits() []EditRecord {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]EditRecord, len(m.Edits))
	copy(out, m.Edits)
	return out
}

func (m *MockBotAPI) SendCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Sends)
}

func (m *MockBotAPI) EditCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.Edits)
}

// Compile-time assertion: MockBotAPI реализует maxapi.BotAPI.
var _ maxapi.BotAPI = (*MockBotAPI)(nil)
