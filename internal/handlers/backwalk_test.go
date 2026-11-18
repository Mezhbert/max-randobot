package handlers_test

// TestWizard_BackWalk_AllSteps — верификация пункта «откат по шагам»:
// полный проход чат-визарда вперёд (10 шагов), затем «← Назад» на КАЖДОМ
// шаге до первого. На каждом откате проверяем, что шаг ДЕЙСТВИТЕЛЬНО
// сместился на предыдущий (не завис, не перескочил).

import (
	"context"
	"strconv"
	"testing"

	"github.com/maxbot/giveawaybot/internal/fsm"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/testutil"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestWizard_BackWalk_AllSteps(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 7100
	testutil.EnsureUser(t, s, uid, "backwalker")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 9200, Title: "BackCh", IsBotAdmin: true})
	require.NoError(t, err)

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	curStep := func() string {
		gg, e := s.GetDraftGiveaway(ctx, uid)
		require.NoError(t, e)
		return gg.DraftStep
	}

	// ---- вперёд по всем 10 шагам ----
	click(util.CBCreateGiveaway)
	require.Equal(t, fsm.StepPost, curStep())
	b.Handle(ctx, msgUpdate(uid, "Текст поста")) // post -> channel
	require.Equal(t, fsm.StepChannel, curStep())
	click(util.CBFSMOption + "ch:" + strconv.FormatInt(ch.ID, 10))
	click(util.CBFSMNext) // channel -> name
	require.Equal(t, fsm.StepName, curStep())
	click(util.CBFSMOption + "name:0")
	click(util.CBFSMNext) // name -> subs
	require.Equal(t, fsm.StepSubs, curStep())
	click(util.CBFSMNext) // subs (канал публикации уже в подписках) -> button
	require.Equal(t, fsm.StepButton, curStep())
	click(util.CBFSMOption + "btn:0")
	click(util.CBFSMNext) // button -> winners
	require.Equal(t, fsm.StepWinners, curStep())
	click(util.CBFSMOption + "win:1")
	click(util.CBFSMNext) // winners -> finish
	require.Equal(t, fsm.StepFinish, curStep())
	click(util.CBFSMOption + "fin:0") // быстрая дата итогов
	click(util.CBFSMNext)             // finish -> start
	require.Equal(t, fsm.StepStart, curStep())
	click(util.CBFSMOption + "start:now")
	click(util.CBFSMNext) // start -> extras
	require.Equal(t, fsm.StepExtras, curStep())
	click(util.CBFSMOption + "cap:all")
	click(util.CBFSMNext) // extras -> check
	require.Equal(t, fsm.StepCheck, curStep(), "прошли все шаги до проверки")

	// ---- Назад на КАЖДОМ шаге: check → post ----
	backChain := []string{
		fsm.StepExtras, fsm.StepStart, fsm.StepFinish, fsm.StepWinners,
		fsm.StepButton, fsm.StepSubs, fsm.StepName, fsm.StepChannel, fsm.StepPost,
	}
	for _, want := range backChain {
		click(util.CBFSMBack)
		assert.Equal(t, want, curStep(), "«← Назад»: ожидали шаг %s", want)
	}

	// ---- значения не потерялись при откатах ----
	gg, err := s.GetDraftGiveaway(ctx, uid)
	require.NoError(t, err)
	assert.Equal(t, "Текст поста", gg.PostText, "текст поста цел после полного отката")
	assert.NotEmpty(t, gg.Name, "название цело")
	assert.Equal(t, 1, gg.WinnersCount, "победители целы")

	// ---- снова вперёд: после полного отката визард проходим до check ----
	click(util.CBFSMNext) // post -> channel
	require.Equal(t, fsm.StepChannel, curStep())
	click(util.CBFSMNext) // channel (выбран) -> name
	require.Equal(t, fsm.StepName, curStep())
	click(util.CBFSMNext) // name (заполнен) -> subs
	require.Equal(t, fsm.StepSubs, curStep())
	click(util.CBFSMNext)
	require.Equal(t, fsm.StepButton, curStep())
	click(util.CBFSMNext)
	require.Equal(t, fsm.StepWinners, curStep())
	click(util.CBFSMNext)
	require.Equal(t, fsm.StepFinish, curStep())
	click(util.CBFSMNext)
	require.Equal(t, fsm.StepStart, curStep())
	click(util.CBFSMNext)
	require.Equal(t, fsm.StepExtras, curStep())
	click(util.CBFSMNext)
	require.Equal(t, fsm.StepCheck, curStep(), "повторный проход после отката достигает проверки")
}

// TestUTMWizard_BackWalk — «← Назад» в визарде трекинг-ссылки на каждом шаге.
func TestUTMWizard_BackWalk(t *testing.T) {
	b, s, _, cleanup := newBot(t)
	defer cleanup()
	ctx := context.Background()
	const uid = 7101
	testutil.EnsureUser(t, s, uid, "utmback")
	ch, err := s.AddChannel(ctx, store.Channel{OwnerUserID: uid, ChatID: 9201, Title: "UTMCh", IsBotAdmin: true})
	require.NoError(t, err)

	click := func(p string) { b.Handle(ctx, callbackUpdate(uid, p)) }
	send := func(text string) { b.Handle(ctx, msgUpdate(uid, text)) }
	stage := func() string {
		sess, e := s.GetSessionPayload(ctx, uid)
		require.NoError(t, e)
		if sess.State != store.SessionUTMCreate {
			return ""
		}
		st, _ := sess.Payload["stage"].(string)
		return st
	}

	click(util.CBUTMQuickCreate)
	require.Equal(t, "mode", stage(), "старт — выбор механики")
	click(util.CBUTMMode + "ads")
	require.Equal(t, "channel", stage(), "реклама — выбор канала")
	click(util.CBUTMQuickChSel + strconv.FormatInt(ch.ID, 10))
	click(util.CBUTMReady) // к названию
	require.Equal(t, "name", stage())
	send("Ссылка QA")
	require.Equal(t, "cost", stage(), "название принято — стоимость")

	// Назад: стоимость → название; снова вперёд.
	click(util.CBUTMBack)
	assert.Equal(t, "name", stage(), "откат: стоимость → название")
	send("Ссылка QA")
	require.Equal(t, "cost", stage())

	// Пропуск стоимости → тип ссылки (публичный: уникальная/супер).
	click(util.CBUTMSkipCost)
	require.Equal(t, "linktype", stage(), "после стоимости — выбор типа")
	// Назад с выбора типа → снова стоимость → вперёд.
	click(util.CBUTMBack)
	assert.Equal(t, "cost", stage(), "откат: тип → стоимость")
	send("1500")
	require.Equal(t, "linktype", stage(), "снова выбор типа")
	// Уникальная ссылка — генерация.
	click(util.CBUTMType + "unique")

	links, err := s.ListUTMStats(ctx, uid, ch.ID)
	require.NoError(t, err)
	require.NotEmpty(t, links, "ссылка сгенерирована после откатов")
	assert.Equal(t, "Ссылка QA", links[0].Source, "название пережило откаты")
}
