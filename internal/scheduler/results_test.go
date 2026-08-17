package scheduler

import (
	"strings"
	"testing"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/stretchr/testify/assert"
)

func TestBuildResultsText_DefaultHeader(t *testing.T) {
	g := store.Giveaway{}
	out := BuildResultsText(&g, []Winner{{Name: "alice", UserID: 1}, {Name: "bob", UserID: 2}})
	assert.Contains(t, out, "🏁 Итоги розыгрыша")
	assert.Contains(t, out, "Победители (2)")
	// Упоминания кликабельные markdown.
	assert.Contains(t, out, "[alice](max://user/1)")
	assert.Contains(t, out, "[bob](max://user/2)")
}

func TestBuildResultsText_CustomHeader(t *testing.T) {
	g := store.Giveaway{ResultsPostText: "🏆 Победители мега-розыгрыша"}
	out := BuildResultsText(&g, []Winner{{Name: "x", UserID: 9}})
	assert.Contains(t, out, "🏆 Победители мега-розыгрыша")
	assert.Contains(t, out, "Победители (1): [x](max://user/9)")
}

func TestBuildResultsText_ManyWinnersUseMultilineSeparator(t *testing.T) {
	ws := []Winner{}
	for i := 1; i <= 7; i++ {
		ws = append(ws, Winner{Name: string(rune('a' + i)), UserID: int64(i)})
	}
	g := store.Giveaway{}
	out := BuildResultsText(&g, ws)
	assert.True(t, strings.Contains(out, "\n• "), "ожидали многострочный список: %q", out)
}

func TestBuildResultsText_FewWinnersUseComma(t *testing.T) {
	g := store.Giveaway{}
	out := BuildResultsText(&g, []Winner{{Name: "a", UserID: 1}, {Name: "b", UserID: 2}})
	assert.Contains(t, out, ", ")
	assert.False(t, strings.Contains(out, "\n• "))
}

func TestBuildResultsText_EmptyWinners(t *testing.T) {
	g := store.Giveaway{}
	out := BuildResultsText(&g, nil)
	assert.Contains(t, out, "Победители (0)")
}

// TestBuildResultsAppended_PreservesOriginalText — оригинальный текст поста
// сохраняется, итоги дописываются с кликабельными победителями.
func TestBuildResultsAppended_PreservesOriginalText(t *testing.T) {
	g := store.Giveaway{PostText: "🎉 Мега-розыгрыш призов!\n\nУсловия: подпишись и участвуй."}
	out := BuildResultsAppended(&g, []Winner{{Name: "w1", UserID: 11}, {Name: "w2", UserID: 22}})

	assert.Contains(t, out, "🎉 Мега-розыгрыш призов!")
	assert.Contains(t, out, "Условия: подпишись и участвуй.")
	assert.Contains(t, out, "🏁 Итоги!")
	assert.Contains(t, out, "Победители (2)")
	assert.Contains(t, out, "[w1](max://user/11)")
	assert.Contains(t, out, "[w2](max://user/22)")
	assert.Contains(t, out, "———")
}

// TestBuildResultsAppended_EmptyPostText — если оригинал пуст, только итоги.
func TestBuildResultsAppended_EmptyPostText(t *testing.T) {
	g := store.Giveaway{}
	out := BuildResultsAppended(&g, []Winner{{Name: "x", UserID: 1}})
	assert.Contains(t, out, "🏁 Итоги!")
	assert.Contains(t, out, "[x](max://user/1)")
	assert.False(t, strings.Contains(out, "———"), "без оригинала нет разделителя")
}

// TestBuildResultsAppended_ManyWinners — многострочный список при >5.
func TestBuildResultsAppended_ManyWinners(t *testing.T) {
	g := store.Giveaway{PostText: "Пост"}
	ws := []Winner{}
	for i := 1; i <= 7; i++ {
		ws = append(ws, Winner{Name: string(rune('a' + i)), UserID: int64(i)})
	}
	out := BuildResultsAppended(&g, ws)
	assert.Contains(t, out, "Пост")
	assert.True(t, strings.Contains(out, "\n• "), "ожидали многострочный список")
}

// TestMentionLink — формат markdown-упоминания.
func TestMentionLink(t *testing.T) {
	assert.Equal(t, "[Иван](max://user/42)", mentionLink(Winner{Name: "Иван", UserID: 42}))
}
