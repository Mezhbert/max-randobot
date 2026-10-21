package fsm

import (
	"testing"
	"time"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/stretchr/testify/assert"
)

func TestOrderedStepsContainsAll(t *testing.T) {
	want := []string{
		StepName, StepPostText, StepMedia, StepButtonText,
		StepSubChannels, StepPubChannels,
		StepPublishMode, StepPublishAt,
		StepFinishMode, StepFinishAt, StepFinishCount,
		StepWinners, StepWinnersMsg, StepReferral, StepResultsLoc, StepResultsText,
		StepCaptcha, StepPreview,
	}
	assert.Equal(t, want, orderedSteps)
}

func TestFirstLast(t *testing.T) {
	assert.Equal(t, StepName, FirstStep)
	assert.Equal(t, StepPreview, orderedSteps[len(orderedSteps)-1])
}

func TestNextStep(t *testing.T) {
	cases := map[string]string{
		StepName:        StepPostText,
		StepPostText:    StepMedia,
		StepMedia:       StepButtonText,
		StepPubChannels: StepPublishMode,
		StepPublishMode: StepPublishAt,
		StepPublishAt:   StepFinishMode,
		StepFinishMode:  StepFinishAt,
		StepFinishAt:    StepFinishCount,
		StepFinishCount: StepWinners,
		StepResultsLoc:  StepResultsText,
		StepResultsText: StepCaptcha,
		StepCaptcha:     StepPreview,
	}
	for from, want := range cases {
		assert.Equal(t, want, NextStep(from), "NextStep(%s)", from)
	}
	assert.Equal(t, "", NextStep(StepPreview), "после preview перехода нет")
	assert.Equal(t, "", NextStep("nope"), "неизвестный шаг → пусто")
}

func TestPrevStep(t *testing.T) {
	assert.Equal(t, StepPostText, PrevStep(StepMedia))
	assert.Equal(t, StepResultsLoc, PrevStep(StepResultsText))
	assert.Equal(t, StepReferral, PrevStep(StepResultsLoc))
	assert.Equal(t, "", PrevStep(StepName), "до первого шага нет предыдущего")
}

func TestIsOptional(t *testing.T) {
	assert.True(t, IsOptional(StepMedia))
	assert.True(t, IsOptional(StepButtonText))
	assert.True(t, IsOptional(StepWinnersMsg))
	assert.True(t, IsOptional(StepResultsText))
	assert.True(t, IsOptional(StepCaptcha))
	assert.False(t, IsOptional(StepPostText))
	assert.False(t, IsOptional(StepName))
	assert.False(t, IsOptional(StepPubChannels))
	assert.False(t, IsOptional(StepPublishAt))
	assert.False(t, IsOptional(StepFinishAt))
	assert.False(t, IsOptional(StepWinners))
	assert.False(t, IsOptional(StepResultsLoc))
	assert.False(t, IsOptional(StepPreview))
}

func TestValidate_OK(t *testing.T) {
	pub := time.Now().Add(2 * time.Hour) // будущее
	fin := pub.Add(time.Hour)
	g := store.Giveaway{
		PostText:          "Текст",
		Name:              "Название",
		ButtonText:        "Участвовать",
		PublishChannelIDs: []int64{1},
		PublishAt:         &pub,
		FinishAt:          &fin,
		WinnersCount:      3,
	}
	assert.NoError(t, validate(g))
}

func TestValidate_Errors(t *testing.T) {
	pub := time.Now().Add(2 * time.Hour) // будущее
	fin := pub.Add(time.Hour)
	base := store.Giveaway{
		PostText: "x", Name: "x", ButtonText: "x",
		PublishChannelIDs: []int64{1}, PublishAt: &pub, FinishAt: &fin, WinnersCount: 1,
	}

	cases := []struct {
		name  string
		mut   func(*store.Giveaway)
	}{
		{"post_text", func(g *store.Giveaway) { g.PostText = "" }},
		{"name", func(g *store.Giveaway) { g.Name = "" }},
		{"button_text", func(g *store.Giveaway) { g.ButtonText = "" }},
		{"channels", func(g *store.Giveaway) { g.PublishChannelIDs = nil }},
		{"publish_at", func(g *store.Giveaway) { g.PublishAt = nil }},
		{"finish_at", func(g *store.Giveaway) { g.FinishAt = nil }},
		{"winners_too_few", func(g *store.Giveaway) { g.WinnersCount = 0 }},
		{"winners_too_many", func(g *store.Giveaway) { g.WinnersCount = 101 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			g := base
			c.mut(&g)
			err := validate(g)
			if err == nil {
				t.Fatalf("ожидалась ошибка валидации для %s", c.name)
			}
			// Должна быть ValidationError.
			ve, ok := err.(*ValidationError)
			assert.True(t, ok, "ожидался *ValidationError, получили %T (%v)", err, err)
			assert.NotEmpty(t, ve.Msg)
		})
	}
}

func TestValidate_FinishBeforePublish(t *testing.T) {
	pub := time.Date(2026, 1, 2, 12, 0, 0, 0, time.UTC)
	fin := time.Date(2026, 1, 1, 12, 0, 0, 0, time.UTC) // раньше публикации
	g := store.Giveaway{
		PostText: "x", Name: "x", ButtonText: "x",
		PublishChannelIDs: []int64{1}, PublishAt: &pub, FinishAt: &fin, WinnersCount: 1,
	}
	err := validate(g)
	assert.Error(t, err)
	assert.IsType(t, &ValidationError{}, err)
}

func TestPromptForStep_AllSteps(t *testing.T) {
	// Каждый шаг имеет непустой промпт (кроме неизвестных).
	for _, s := range orderedSteps {
		assert.NotEmpty(t, PromptForStep(s), "пустой промпт для %s", s)
	}
	assert.Empty(t, PromptForStep("nope"), "неизвестный шаг → пустой промпт")
}
