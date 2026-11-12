package fsm

import (
	"context"
	"testing"
	"time"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/stretchr/testify/assert"
)

// TestOrderedSteps — 10 шагов мануала, в точном порядке.
func TestOrderedSteps(t *testing.T) {
	want := []string{
		StepPost, StepChannel, StepName, StepSubs, StepButton,
		StepWinners, StepFinish, StepStart, StepExtras, StepCheck,
	}
	assert.Equal(t, want, orderedSteps)
	assert.Equal(t, 10, TotalSteps())
}

func TestFirstLast(t *testing.T) {
	assert.Equal(t, StepPost, FirstStep)
	assert.Equal(t, StepCheck, orderedSteps[len(orderedSteps)-1])
	assert.Equal(t, "", PrevStep(StepPost), "до первого шага нет предыдущего")
	assert.Equal(t, "", NextStep(StepCheck), "после проверки перехода нет")
}

func TestNextPrev(t *testing.T) {
	for i, s := range orderedSteps {
		if i+1 < len(orderedSteps) {
			assert.Equal(t, orderedSteps[i+1], NextStep(s), "NextStep(%s)", s)
		}
		if i > 0 {
			assert.Equal(t, orderedSteps[i-1], PrevStep(s), "PrevStep(%s)", s)
		}
	}
	assert.Equal(t, "", NextStep("nope"))
	assert.Equal(t, "", PrevStep("nope"))
}

// TestNormalizeStep — legacy-шаги старого мастера мапятся на ближайший шаг
// новой схемы; мусор — на первый.
func TestNormalizeStep(t *testing.T) {
	cases := map[string]string{
		"post_text": StepPost, "media": StepPost,
		"pub_channels": StepChannel, "sub_channels": StepSubs,
		"publish_mode": StepStart, "finish_at": StepFinish,
		"winners_msg": StepExtras, "preview": StepCheck,
		"name": StepName, StepPost: StepPost,
		"garbage": FirstStep, "": FirstStep,
	}
	for in, want := range cases {
		assert.Equal(t, want, NormalizeStep(in), "NormalizeStep(%q)", in)
	}
}

func TestProgressBar(t *testing.T) {
	assert.Equal(t, "▱▱▱▱▱▱▱▱▱▱", ProgressBar(0, 10))
	assert.Equal(t, "▰▰▰▰▰▰▱▱▱▱", ProgressBar(6, 10))
	assert.Equal(t, "▰▰▰▰▰▰▰▰▰▰", ProgressBar(10, 10))
	assert.Equal(t, "▰▰▰▰▰▰▰▰▰▰", ProgressBar(12, 10), "перебор обрезается")
}

func TestValidate_OK(t *testing.T) {
	pub := time.Now().Add(2 * time.Hour)
	fin := pub.Add(time.Hour)
	g := store.Giveaway{
		PostText: "Текст", Name: "Название", ButtonText: "Участвовать",
		PublishChannelIDs: []int64{1}, PublishAt: &pub, FinishAt: &fin, WinnersCount: 3,
	}
	assert.NoError(t, validate(g))

	// PublishNow: publish_at не нужен (ставится в now при Confirm).
	g2 := g
	g2.PublishNow = true
	g2.PublishAt = nil
	assert.NoError(t, validate(g2))

	// Медиа без текста — валидный пост (мануал: «≥ 10 символов ИЛИ медиа»).
	g3 := g
	g3.PostText = ""
	g3.MediaTokens = []store.MediaToken{{Token: "t", Type: "image"}}
	assert.NoError(t, validate(g3))
}

func TestValidate_Errors(t *testing.T) {
	pub := time.Now().Add(2 * time.Hour)
	fin := pub.Add(time.Hour)
	base := store.Giveaway{
		PostText: "x", Name: "x", ButtonText: "x",
		PublishChannelIDs: []int64{1}, PublishAt: &pub, FinishAt: &fin, WinnersCount: 1,
	}
	cases := []struct {
		name string
		mut  func(*store.Giveaway)
	}{
		{"post", func(g *store.Giveaway) { g.PostText = ""; g.MediaTokens = nil }},
		{"name", func(g *store.Giveaway) { g.Name = "" }},
		{"button_text", func(g *store.Giveaway) { g.ButtonText = "" }},
		{"channels", func(g *store.Giveaway) { g.PublishChannelIDs = nil }},
		{"publish_at", func(g *store.Giveaway) { g.PublishAt = nil }},
		{"finish_at", func(g *store.Giveaway) { g.FinishAt = nil }},
		{"winners_too_few", func(g *store.Giveaway) { g.WinnersCount = 0 }},
		{"winners_too_many", func(g *store.Giveaway) { g.WinnersCount = 101 }},
		{"finish_count_lt_10", func(g *store.Giveaway) {
			g.FinishMode = "count"
			g.FinishParticipantsCount = 5
		}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := base
			c.mut(&g)
			err := validate(g)
			if err == nil {
				t.Fatalf("ожидалась ошибка валидации для %s", c.name)
			}
			ve, ok := err.(*ValidationError)
			assert.True(t, ok, "ожидался *ValidationError, получили %T (%v)", err, err)
			assert.NotEmpty(t, ve.Msg)
		})
	}
}

func TestValidate_FinishBeforePublish(t *testing.T) {
	pub := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	fin := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC)
	g := store.Giveaway{
		PostText: "x", Name: "x", ButtonText: "x",
		PublishChannelIDs: []int64{1}, PublishAt: &pub, FinishAt: &fin, WinnersCount: 1,
	}
	err := validate(g)
	assert.Error(t, err)
	assert.IsType(t, &ValidationError{}, err)
}

// TestStepText_HeaderAndBar — текст каждого шага несёт «Шаг N из 10», бар
// и вопрос.
func TestStepText_HeaderAndBar(t *testing.T) {
	e := &Engine{}
	for _, s := range orderedSteps {
		txt := e.StepText(context.Background(), store.Giveaway{}, s)
		assert.Contains(t, txt, "Шаг ", s)
		assert.Contains(t, txt, " из 10", s)
		assert.Contains(t, txt, "▰", s)
		assert.NotEmpty(t, StepQuestion(s), s)
		assert.LessOrEqual(t, len(HintForStep(s)), 150, "подсказка ≤150 символов: %s", s)
	}
}

// TestNameVariants — варианты названия строятся из первой строки поста.
func TestNameVariants(t *testing.T) {
	g := store.Giveaway{PostText: "iPhone 15 в декабре!\nПодписывайтесь"}
	v := NameVariants(g, "Мой канал")
	require := assert.New(t)
	require.Len(v, 2, "с каналом — два варианта")
	require.Contains(v[0], "iPhone 15 в декабре!")
	require.Contains(v[1], "Мой канал")
}

// TestExtrasStateText — блок состояния шага 9 показывает все функции.
func TestExtrasStateText(t *testing.T) {
	g := store.Giveaway{CaptchaMode: store.CaptchaAll, ReferralEnabled: true, ReferralLimit: 3,
		WinnersMessage: "пишите", ResultsInOriginalPost: true}
	txt := ExtrasStateText(g)
	assert.Contains(t, txt, "Капча:** ✅")
	assert.Contains(t, txt, "до 3 друзей")
	assert.Contains(t, txt, "Сообщение победителю:** добавлено")
	assert.Contains(t, txt, "Итоги:** в исходном посте")
}
