package store

import (
	"time"

	"github.com/jackc/pgx/v5/pgtype"
)

// Статусы розыгрыша (см. giveaway_status в БД).
const (
	StatusDraft      = "draft"      // в процессе создания (черновик)
	StatusScheduled  = "scheduled"  // запланированный
	StatusPublishing = "publishing" // захвачен планировщиком для публикации
	StatusActive     = "active"     // текущий (опубликован)
	StatusFinishing  = "finishing"  // захвачен планировщиком для подведения итогов
	StatusFinished   = "finished"   // завершённый
	StatusCancelled  = "cancelled"  // отменён
)

// InProcessStatuses — статусы «захвачено планировщиком» (atomic claim).
// Пока розыгрыш в одном из них, его не подберёт ни другой тик, ни другой инстанс.
var InProcessStatuses = map[string]bool{
	StatusPublishing: true,
	StatusFinishing:  true,
}

// Режимы капчи (captcha_mode).
const (
	CaptchaNone    = "none"
	CaptchaAll     = "all"
	CaptchaPercent = "percent"
)

// MediaToken — одно медиа-вложение поста розыгрыша.
type MediaToken struct {
	Token string `json:"token"`
	Type  string `json:"type"` // image | video
}

// User — пользователь MAX.
type User struct {
	UserID        int64
	FirstName     string
	Username      string
	IsBot         bool
	FlagInBase    bool
	DialogChatID  int64 // chat_id диалога bot↔user (для GetMessages при /clear)
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Channel — канал создателя ("мои каналы").
type Channel struct {
	ID            int64
	OwnerUserID   int64
	ChatID        int64
	Title         string
	Link          string
	IsPublic      bool
	IsBotAdmin    bool
	AdminCheckedAt *time.Time
	CreatedAt     time.Time
	UpdatedAt     time.Time
}

// Giveaway — розыгрыш. Поля соответствуют этапам 1-13.
type Giveaway struct {
	ID                     int64
	OwnerUserID            int64
	Name                   string
	PostText               string
	PostFormat             string
	MediaTokens            []MediaToken
	ButtonText             string
	SubscribeChannelIDs    []int64
	PublishChannelIDs      []int64
	ResultsInOriginalPost  bool
	ResultsPostText        string
	PublishAt              *time.Time
	FinishAt               *time.Time
	FinishMode             string // "time" (по времени) | "count" (по количеству участников)
	FinishParticipantsCount int   // целевое кол-во участников для finish_mode=count
	WinnersCount           int
	WinnersMessage         string
	ReferralEnabled        bool
	ReferralLimit          int
	CaptchaMode            string
	CaptchaPercent         int
	PromotionEnabled       bool
	DraftStep              string // текущий шаг FSM (для черновиков)
	Status                 string
	PublishMessageIDs      map[int64]string // chat_id -> mid
	ResultsMessageIDs      map[int64]string
	WinnersUserIDs         []int64
	WinnersNotifiedUserIDs []int64 // кому из победителей уже отправлено ЛС (идемпотентность finish)
	ResultsPublished       bool    // итоги уже опубликованы (обе ветки: edit поста / отдельный пост)
	ChannelSelectMID       string // mid сообщения выбора каналов (для edit-in-place тоггла)
	CreatedAt              time.Time
	UpdatedAt              time.Time

	// Приватные «сырые» поля — приёмники под Scan, расшифровываются в finalizeScan.
	// Хранятся в структуре, чтобы scanDest() возвращал указатели на валидные адреса.
	draftStep         pgtype.Text
	channelSelectMID  pgtype.Text
	publishAt         pgtype.Timestamptz
	finishAt          pgtype.Timestamptz
	mediaTokensRaw    []byte
	publishMsgs       []byte
	resultsMsgs       []byte
}

// Participant — участник розыгрыша.
type Participant struct {
	ID            int64
	GiveawayID    int64
	UserID        int64
	ReferralBy    int64 // 0 = нет
	Tickets       int
	CaptchaPassed bool
	JoinedAt      time.Time
}
