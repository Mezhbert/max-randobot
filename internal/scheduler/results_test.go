package scheduler

import (
	"strings"
	"testing"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/stretchr/testify/assert"
)

func TestBuildResultsText_DefaultHeader(t *testing.T) {
	g := store.Giveaway{} // пустой ResultsPostText
	out := BuildResultsText(&g, []string{"@alice", "@bob"})
	assert.Contains(t, out, "🏁 Итоги розыгрыша")
	assert.Contains(t, out, "Победители (2)")
	assert.Contains(t, out, "@alice, @bob")
}

func TestBuildResultsText_CustomHeader(t *testing.T) {
	g := store.Giveaway{ResultsPostText: "🏆 Победители мега-розыгрыша"}
	out := BuildResultsText(&g, []string{"@x"})
	assert.Contains(t, out, "🏆 Победители мега-розыгрыша")
	assert.Contains(t, out, "Победители (1): @x")
}

func TestBuildResultsText_ManyWinnersUseMultilineSeparator(t *testing.T) {
	names := []string{"a", "b", "c", "d", "e", "f", "g"} // 7 > 5
	g := store.Giveaway{}
	out := BuildResultsText(&g, names)
	// При >5 победителях разделитель — перенос строки с буллитом.
	assert.True(t, strings.Contains(out, "\n• "), "ожидали многострочный список: %q", out)
}

func TestBuildResultsText_FewWinnersUseComma(t *testing.T) {
	g := store.Giveaway{}
	out := BuildResultsText(&g, []string{"a", "b"})
	assert.Contains(t, out, "a, b")
	assert.False(t, strings.Contains(out, "\n• "))
}

func TestBuildResultsText_EmptyWinners(t *testing.T) {
	g := store.Giveaway{}
	out := BuildResultsText(&g, nil)
	assert.Contains(t, out, "Победители (0)")
}

// TestBuildResultsAppended_PreservesOriginalText — оригинальный текст поста
// сохраняется, итоги дописываются в конце.
func TestBuildResultsAppended_PreservesOriginalText(t *testing.T) {
	g := store.Giveaway{PostText: "🎉 Мега-розыгрыш призов!\n\nУсловия: подпишись и участвуй."}
	out := BuildResultsAppended(&g, []string{"@winner1", "@winner2"})

	// Оригинальный текст сохранён.
	assert.Contains(t, out, "🎉 Мега-розыгрыш призов!")
	assert.Contains(t, out, "Условия: подпишись и участвуй.")
	// Итоги дописаны.
	assert.Contains(t, out, "🏁 Итоги!")
	assert.Contains(t, out, "Победители (2)")
	assert.Contains(t, out, "@winner1, @winner2")
	// Разделитель между оригиналом и итогами.
	assert.Contains(t, out, "———")
}

// TestBuildResultsAppended_EmptyPostText — если оригинал пуст, только итоги.
func TestBuildResultsAppended_EmptyPostText(t *testing.T) {
	g := store.Giveaway{}
	out := BuildResultsAppended(&g, []string{"@x"})
	assert.Contains(t, out, "🏁 Итоги!")
	assert.Contains(t, out, "@x")
	assert.False(t, strings.Contains(out, "———"), "без оригинала нет разделителя")
}

// TestBuildResultsAppended_ManyWinners — многострочный список при >5.
func TestBuildResultsAppended_ManyWinners(t *testing.T) {
	g := store.Giveaway{PostText: "Пост"}
	names := []string{"a", "b", "c", "d", "e", "f", "g"}
	out := BuildResultsAppended(&g, names)
	assert.Contains(t, out, "Пост")
	assert.True(t, strings.Contains(out, "\n• "), "ожидали многострочный список")
}
