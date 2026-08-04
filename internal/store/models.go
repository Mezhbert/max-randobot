package store

import "time"

// Статусы розыгрыша (см. giveaway_status в БД).
const (
	StatusDraft     = "draft"     // в процессе создания (черновик)
	StatusScheduled = "scheduled" // запланированный
	StatusActive    = "active"    // текущий (опубликован)
	StatusFinished  = "finished"  // завершённый
	StatusCancelled = "cancelled" // отменён
)

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
	UserID      int64
	FirstName   string
	Username    string
	IsBot       bool
	FlagInBase  bool
	CreatedAt   time.Time
	UpdatedAt   time.Time
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
	CreatedAt              time.Time
	UpdatedAt              time.Time
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
