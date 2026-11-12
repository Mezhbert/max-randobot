package handlers

// Мастер создания розыгрыша — интерфейс по мануалу RandoMaster_MAX
// (dev-notes/Claude-log): 10 шагов, «Один шаг — одно сообщение»,
// прогресс-бар, первый вариант предвыбран «✓», нажатия морфируют сообщение
// шага (AnswerCallbackEdit), пройденный шаг стирает клавиатуру и получает
// «✓ ответ», следующий шаг приходит новым сообщением.

import (
	"context"
	_ "embed"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/maxbot/giveawaybot/internal/fsm"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

//go:embed assets/admin-rights.png
var adminRightsPNG []byte

// postExampleText — текст кнопки «Вставить пример» (шаг 1).
const postExampleText = "🎁 РОЗЫГРЫШ!\n\nУсловия простые:\n• Подпишись на каналы из списка ниже\n• Нажми «Участвовать»\n\n📊 Итоги подведём честно — в прямом эфире. Победителей выберет бот случайно среди всех участников.\n\nУдачи! 🍀"

// ---------- рендер шага ----------

// stepKeyboard — клавиатура шага: варианты (выбранный с «✓ ») → «Далее →» →
// служебный ряд «💡 Справка · ← Назад · ✕ Отмена» (мануал §3).
func (b *Bot) stepKeyboard(ctx context.Context, uid int64, g store.Giveaway, step string) *model.Keyboard {
	kb := model.NewKeyboard()
	switch step {
	case fsm.StepPost:
		kb.AddRow().AddCallBack("📝 Вставить пример", util.CBFSMOption+"post_example")
		if b.engine.StepComplete(ctx, g, step) {
			kb.AddRow().AddCallBack("Далее →", util.CBFSMNext)
		}
		util.AddServiceRow(kb, false)

	case fsm.StepChannel:
		channels, _ := b.db.ListChannels(ctx, uid)
		for _, c := range channels {
			title := channelLabel(c)
			mark := "○ "
			if len(g.PublishChannelIDs) == 1 && g.PublishChannelIDs[0] == c.ID {
				mark = "✓ "
			}
			kb.AddRow().AddCallBack(mark+title, util.CBFSMOption+"ch:"+strconv.FormatInt(c.ID, 10))
		}
		kb.AddRow().AddCallBack("➕ Подключить канал", util.CBChannelAdd)
		if len(g.PublishChannelIDs) > 0 {
			kb.AddRow().AddCallBack("Далее →", util.CBFSMNext)
		}
		util.AddServiceRow(kb, true)

	case fsm.StepName:
		for i, v := range b.nameVariants(ctx, g) {
			mark := "○ "
			if g.Name == v {
				mark = "✓ "
			}
			kb.AddRow().AddCallBack(mark+v, util.CBFSMOption+fmt.Sprintf("name:%d", i))
		}
		if g.Name != "" {
			kb.AddRow().AddCallBack("Далее →", util.CBFSMNext)
		}
		util.AddServiceRow(kb, true)

	case fsm.StepSubs:
		channels, _ := b.db.ListChannels(ctx, uid)
		pub := map[int64]bool{}
		for _, id := range g.PublishChannelIDs {
			pub[id] = true
		}
		sel := map[int64]bool{}
		for _, id := range g.SubscribeChannelIDs {
			sel[id] = true
		}
		n := 0
		for _, c := range channels {
			title := channelLabel(c)
			if pub[c.ID] {
				// Канал публикации предвыбран и не снимается (мануал §4 шаг 4).
				kb.AddRow().AddCallBack("✓ "+title+" 🔒", util.CBFSMOption+"locked")
				n++
				continue
			}
			mark := "⬜ "
			if sel[c.ID] {
				mark = "✅ "
				n++
			}
			kb.AddRow().AddCallBack(mark+title, util.CBChannelToggleSub+strconv.FormatInt(c.ID, 10))
		}
		kb.AddRow().AddCallBack("➕ Подключить канал", util.CBChannelAdd)
		kb.AddRow().AddCallBack(fmt.Sprintf("Далее · выбрано %d", n), util.CBFSMNext)
		util.AddServiceRow(kb, true)

	case fsm.StepButton:
		variants := []string{"Участвовать", "Участвую!", "Хочу приз", "Мне повезёт"}
		for i, v := range variants {
			mark := "○ "
			if strings.EqualFold(g.ButtonText, v) {
				mark = "✓ "
			}
			kb.AddRow().AddCallBack(mark+v, util.CBFSMOption+fmt.Sprintf("btn:%d", i))
		}
		if g.ButtonText != "" {
			kb.AddRow().AddCallBack("Далее →", util.CBFSMNext)
		}
		util.AddServiceRow(kb, true)

	case fsm.StepWinners:
		for _, n := range []int{1, 3, 5, 10} {
			mark := "○ "
			if g.WinnersCount == n {
				mark = "✓ "
			}
			kb.AddRow().AddCallBack(fmt.Sprintf("%s%d", mark, n), util.CBFSMOption+fmt.Sprintf("win:%d", n))
		}
		if g.WinnersCount >= 1 {
			kb.AddRow().AddCallBack("Далее →", util.CBFSMNext)
		}
		util.AddServiceRow(kb, true)

	case fsm.StepFinish:
		if g.FinishMode == "count" {
			kb.AddRow().AddCallBack(fmt.Sprintf("✓ 👥 При %d участниках", g.FinishParticipantsCount), util.CBFSMOption+"noop")
			kb.AddRow().AddCallBack("📅 По времени", util.CBFSMOption+"fin_time")
		} else {
			for _, opt := range b.finishQuickOptions() {
				mark := "○ "
				if g.FinishAt != nil && g.FinishAt.Equal(opt.t) {
					mark = "✓ "
				}
				kb.AddRow().AddCallBack(mark+opt.label, util.CBFSMOption+"fin:"+opt.key)
			}
			kb.AddRow().
				AddCallBack("📅 Календарь", util.CBFSMOption+"fin_cal").
				AddCallBack("👥 По числу участников", util.CBFSMOption+"fin_count")
		}
		if b.engine.StepComplete(ctx, g, step) {
			kb.AddRow().AddCallBack("Далее →", util.CBFSMNext)
		}
		util.AddServiceRow(kb, true)

	case fsm.StepStart:
		opts := b.startOptions()
		for _, opt := range opts {
			mark := "○ "
			switch {
			case opt.key == "now" && g.PublishNow:
				mark = "✓ "
			case opt.key != "now" && !g.PublishNow && g.PublishAt != nil && g.PublishAt.Equal(opt.t):
				mark = "✓ "
			}
			kb.AddRow().AddCallBack(mark+opt.label, util.CBFSMOption+"start:"+opt.key)
		}
		if b.engine.StepComplete(ctx, g, step) {
			kb.AddRow().AddCallBack("Далее →", util.CBFSMNext)
		}
		util.AddServiceRow(kb, true)

	case fsm.StepExtras:
		capLabel := "🛡 Капча: ❌ выкл"
		if g.CaptchaMode == store.CaptchaAll {
			capLabel = "🛡 Капча: ✅ вкл"
		}
		kb.AddRow().AddCallBack(capLabel, util.CBFSMOption+"cap")
		frLabel := "👥 Друзья: ❌ выкл"
		if g.ReferralEnabled {
			limit := g.ReferralLimit
			if limit <= 0 {
				limit = 10
			}
			frLabel = fmt.Sprintf("👥 Друзья: ✅ до %d", limit)
		}
		kb.AddRow().AddCallBack(frLabel, util.CBFSMOption+"fr")
		if g.ReferralEnabled {
			row := kb.AddRow()
			for _, n := range []int{1, 3, 5, 10} {
				mark := "○ "
				if g.ReferralLimit == n || (g.ReferralLimit == 0 && n == 10) {
					mark = "✓ "
				}
				row.AddCallBack(fmt.Sprintf("%s%d", mark, n), util.CBFSMOption+fmt.Sprintf("frlim:%d", n))
			}
		}
		msgLabel := "✉️ Добавить сообщение победителю"
		if g.WinnersMessage != "" {
			msgLabel = "✉️ Изменить сообщение победителю"
		}
		kb.AddRow().AddCallBack(msgLabel, util.CBFSMOption+"msg")
		resLabel := "📍 Итоги: в исходном посте"
		if !g.ResultsInOriginalPost {
			resLabel = "📍 Итоги: отдельным постом"
		}
		kb.AddRow().AddCallBack(resLabel, util.CBFSMOption+"resloc")
		kb.AddRow().AddCallBack("Далее →", util.CBFSMNext)
		util.AddServiceRow(kb, true)

	case fsm.StepCheck:
		for _, s := range []string{
			fsm.StepPost, fsm.StepChannel, fsm.StepName, fsm.StepSubs,
			fsm.StepButton, fsm.StepWinners, fsm.StepFinish, fsm.StepStart, fsm.StepExtras,
		} {
			kb.AddRow().AddCallBack("✏️ "+fsm.StepEmoji(s)+" "+fsm.StepTitle(s), util.CBFSMEdit+s)
		}
		kb.AddRow().
			AddCallBack("🚀 Опубликовать", util.CBFSMConfirm).
			AddCallBack("💾 В черновики", util.CBFSMSave)
		util.AddServiceRow(kb, true)
	}
	return kb
}

// channelLabel — подпись канала для кнопки (⚠️ = бот не админ).
func channelLabel(c store.Channel) string {
	title := c.Title
	if title == "" {
		title = fmt.Sprintf("канал #%d", c.ChatID)
	}
	if !c.IsBotAdmin {
		title += " ⚠️"
	}
	return title
}

// StepKeyboardForTest — клавиатура шага для тестов (пакет handlers_test).
func (b *Bot) StepKeyboardForTest(ctx context.Context, uid int64, g store.Giveaway, step string) *model.Keyboard {
	return b.stepKeyboard(ctx, uid, g, step)
}

// finishQuickOption — быстрая дата итогов (мануал §4 шаг 7).
type finishQuickOption struct {
	key   string
	label string
	t     time.Time
}

// finishQuickOptions — Завтра 21:00 / Через 3 дня / Через неделю / Через 2
// недели (МСК; label совпадает с кнопкой мануала).
func (b *Bot) finishQuickOptions() []finishQuickOption {
	now := time.Now().In(util.MoscowLocation())
	at21 := func(d time.Time) time.Time {
		return time.Date(d.Year(), d.Month(), d.Day(), 21, 0, 0, 0, util.MoscowLocation()).UTC()
	}
	tomorrow := now.AddDate(0, 0, 1)
	return []finishQuickOption{
		{"tom", "Завтра, 21:00", at21(tomorrow)},
		{"d3", "Через 3 дня", at21(now.AddDate(0, 0, 3))},
		{"w1", "Через неделю", at21(now.AddDate(0, 0, 7))},
		{"w2", "Через 2 недели", at21(now.AddDate(0, 0, 14))},
	}
}

// startOption — вариант публикации (шаг 8).
type startOption struct {
	key   string
	label string
	t     time.Time
}

// startOptions — Сразу / Через час / Завтра 10:00.
func (b *Bot) startOptions() []startOption {
	now := time.Now().In(util.MoscowLocation())
	tomorrow := now.AddDate(0, 0, 1)
	return []startOption{
		{"now", "🚀 Сразу", time.Time{}},
		{"hour", "Через час", now.Add(time.Hour).Truncate(time.Minute).UTC()},
		{"tom10", "Завтра, 10:00", time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 10, 0, 0, 0, util.MoscowLocation()).UTC()},
	}
}

// nameVariants — авто-варианты названия: первая строка поста · дата / · канал.
func (b *Bot) nameVariants(ctx context.Context, g store.Giveaway) []string {
	channelTitle := ""
	if len(g.PublishChannelIDs) > 0 {
		if chs, err := b.db.ChannelsByIDs(ctx, g.OwnerUserID, g.PublishChannelIDs); err == nil && len(chs) > 0 {
			channelTitle = chs[0].Title
		}
	}
	return fsm.NameVariants(g, channelTitle)
}

// ---------- показ шага ----------

// showStep — показать шаг: редактирует активное сообщение мастера
// (channel_select_mid), иначе отправляет новое и запоминает mid.
func (b *Bot) showStep(ctx context.Context, uid int64, g store.Giveaway, step string) {
	if step == fsm.StepCheck {
		b.showCheckScreen(ctx, uid, g)
		return
	}
	text := b.engine.StepText(ctx, g, step)
	kb := b.stepKeyboard(ctx, uid, g, step)
	if mid := g.ChannelSelectMID; mid != "" {
		if err := b.api.EditChatText(ctx, mid, text, []model.Attachment{kb.Build()}, model.FormatMarkdown); err == nil {
			return
		}
		// Edit упал (сообщение слишком старое/удалено) — шлём новое.
	}
	mid, err := b.api.SendTextWithKeyboard(ctx, uid, text, kb)
	if err == nil && mid != "" {
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", mid)
	}
}

// showCheckScreen — шаг 10: превью поста точным видом + сводка с правкой
// пунктов (мануал §4 шаг 10: «два сообщения»).
func (b *Bot) showCheckScreen(ctx context.Context, uid int64, g store.Giveaway) {
	buttonText := ifEmpty(g.ButtonText, "Участвовать")
	mediaSimple := make([]util.MediaTokenSimple, 0, len(g.MediaTokens))
	for _, mt := range g.MediaTokens {
		mediaSimple = append(mediaSimple, util.MediaTokenSimple{Type: mt.Type, Token: mt.Token})
	}
	// 1) Превью поста «как будет в канале».
	attachments := util.GiveawayAttachments(buttonText, g.ID, mediaSimple, true)
	_ = b.api.SendWithAttachments(ctx, uid, g.PostText, attachments, model.TextFormat(g.PostFormat))
	// 2) Сводка + правка любого пункта + публикация.
	subs := b.engine.ChannelTitlesLine(ctx, g.OwnerUserID, g.SubscribeChannelIDs)
	pubs := b.engine.ChannelTitlesLine(ctx, g.OwnerUserID, g.PublishChannelIDs)
	text := fsm.StepHeader(fsm.StepCheck) + "\n⬆️ Так будет выглядеть пост (с кнопкой «" + buttonText + "»).\n\n" +
		fsm.FormatCheck(g, subs, pubs)
	mid, _ := b.api.SendTextWithKeyboard(ctx, uid, text, b.stepKeyboard(ctx, uid, g, fsm.StepCheck))
	if mid != "" {
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", mid)
	}
}

// morphStep — морфинг сообщения шага ответом на нажатие ( cbID="" → edit по
// активному mid). Используется при выборе вариантов: «✓ » переезжает.
func (b *Bot) morphStep(ctx context.Context, cbID string, uid int64, g store.Giveaway, step string) {
	text := b.engine.StepText(ctx, g, step)
	kb := b.stepKeyboard(ctx, uid, g, step)
	if cbID != "" {
		b.api.AnswerCallbackEdit(ctx, cbID, text, kb)
		return
	}
	if mid := g.ChannelSelectMID; mid != "" {
		_ = b.api.EditChatText(ctx, mid, text, []model.Attachment{kb.Build()}, model.FormatMarkdown)
		return
	}
	mid, _ := b.api.SendTextWithKeyboard(ctx, uid, text, kb)
	if mid != "" {
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", mid)
	}
}

// morphError — шаг с первой строкой «❌ …» (невалидный ввод, мануал §3).
func (b *Bot) morphError(ctx context.Context, uid int64, g store.Giveaway, errMsg string) {
	text := "❌ " + errMsg + "\n\n" + b.engine.StepText(ctx, g, g.DraftStep)
	kb := b.stepKeyboard(ctx, uid, g, g.DraftStep)
	if mid := g.ChannelSelectMID; mid != "" {
		if err := b.api.EditChatText(ctx, mid, text, []model.Attachment{kb.Build()}, model.FormatMarkdown); err == nil {
			return
		}
	}
	mid, _ := b.api.SendTextWithKeyboard(ctx, uid, text, kb)
	if mid != "" {
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", mid)
	}
}

// passAndAdvance — морфинг пройденного шага («✓ ответ», без клавиатуры) и
// отправка следующего шага новым сообщением (мануал §3 «Далее →»).
// return_to: после правки пункта с экрана проверки «Далее» возвращает на
// проверку (мануал §4 шаг 10).
func (b *Bot) passAndAdvance(ctx context.Context, uid int64, g store.Giveaway, step, cbID string) {
	// Стираем клавиатуру шага, дописываем «✓ ответ».
	passed := b.engine.PassedText(ctx, g, step)
	if cbID != "" {
		b.api.AnswerCallbackEdit(ctx, cbID, passed, nil)
	} else if mid := g.ChannelSelectMID; mid != "" {
		_ = b.api.EditChatText(ctx, mid, passed, nil, model.FormatMarkdown)
	}

	// Правка с экрана проверки: «Далее» возвращает на проверку (мануал §4).
	ret := b.wizardReturnTo(ctx, uid)
	_ = b.db.ClearSession(ctx, uid) // выходим из подэкранов (календарь/поле)
	next := fsm.NextStep(step)
	if ret != "" {
		next = ret
	}
	if next == "" || next == step {
		return
	}
	_ = b.db.SetDraftStep(ctx, g.ID, next)
	gg, err := b.db.GetGiveawayAny(ctx, g.ID)
	if err != nil {
		return
	}
	// Сообщение следующего шага — новое; оно становится активным.
	_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", nil)
	gg.ChannelSelectMID = ""
	b.showStep(ctx, uid, gg, next)
}

// wizardReturnTo — запомненный шаг возврата (правка с экрана проверки).
func (b *Bot) wizardReturnTo(ctx context.Context, uid int64) string {
	sess, err := b.db.GetSessionPayload(ctx, uid)
	if err != nil || sess.State != store.SessionWizard {
		return ""
	}
	v, _ := sess.Payload["return_to"].(string)
	return v
}

// goBack — «← Назад»: предыдущий шаг с сохранёнными значениями; с первого
// шага — в меню (мануал §3).
func (b *Bot) goBack(ctx context.Context, uid int64, g store.Giveaway, cbID string) {
	// Первый шаг: Назад некуда — проходимый вид + меню.
	if fsm.PrevStep(g.DraftStep) == "" {
		b.api.AnswerCallbackEdit(ctx, cbID, b.engine.PassedText(ctx, g, g.DraftStep), nil)
		_ = b.db.ClearSession(ctx, uid)
		b.sendMainMenu(ctx, uid, "Создание приостановлено — черновик в «Мои розыгрыши». Продолжить: ✏️ у черновика.")
		return
	}
	prev, err := b.engine.GoBack(ctx, g.ID, g.DraftStep, g)
	if err != nil {
		b.log.Warn("go back", "err", err, "id", g.ID, "user", uid)
		b.api.AnswerCallback(ctx, cbID, "⚠️ Не удалось вернуться. Попробуйте ещё раз.")
		return
	}
	// Текущий шаг — в пройденный вид («✓ ответ», без клавиатуры).
	passed := b.engine.PassedText(ctx, g, g.DraftStep)
	if cbID != "" {
		b.api.AnswerCallbackEdit(ctx, cbID, passed, nil)
	} else if mid := g.ChannelSelectMID; mid != "" {
		_ = b.api.EditChatText(ctx, mid, passed, nil, model.FormatMarkdown)
	}
	_ = b.db.ClearSession(ctx, uid)
	if prev == g.DraftStep {
		return
	}
	_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", nil)
	gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
	b.showStep(ctx, uid, gg, prev)
}

// ---------- ввод ----------

// handlePostInput — шаг 1: текст и/или фото-видео ОДНИМ сообщением (или
// несколькими: медиа докидывается). Первое заполнение → сразу шаг 2; при
// возврате «Назад» докидывание медиа остаётся на шаге.
func (b *Bot) handlePostInput(ctx context.Context, uid int64, g store.Giveaway, msg model.MessageUpdate) {
	// Форвард чужого поста — не контент розыгрыша (пост — свой текст/медиа;
	// форвард добавляет канал, но это шаг 2).
	if isForward(msg) {
		b.morphError(ctx, uid, g, "Это пересланное сообщение. Пришлите СВОЙ текст и/или фото-видео (форвард для подключения канала — на шаге 2)")
		return
	}
	text := strings.TrimSpace(msg.Body.Text)
	var newTokens []store.MediaToken
	for _, at := range msg.Body.Attachments {
		switch at.Type {
		case model.AttachImage:
			if at.Payload.Token != "" {
				newTokens = append(newTokens, store.MediaToken{Token: at.Payload.Token, Type: "image"})
			}
		case model.AttachVideo:
			if at.Payload.Token != "" {
				newTokens = append(newTokens, store.MediaToken{Token: at.Payload.Token, Type: "video"})
			}
		}
	}

	if text != "" {
		if err := util.ValidatePostText(text); err != nil {
			b.morphError(ctx, uid, g, err.Error())
			return
		}
		if err := b.db.UpdateGiveawayField(ctx, g.ID, "post_text", text); err != nil {
			b.morphError(ctx, uid, g, "Не удалось сохранить текст. Попробуйте позже.")
			return
		}
		g.PostText = text
	}
	if len(newTokens) > 0 {
		all, added := mergeMediaTokens(g.MediaTokens, newTokens)
		if added > 0 {
			if err := b.db.UpdateGiveawayField(ctx, g.ID, "media_tokens", all); err != nil {
				b.morphError(ctx, uid, g, "Не удалось сохранить медиа. Попробуйте позже.")
				return
			}
			g.MediaTokens = all
		}
	}
	if g.PostText == "" && len(g.MediaTokens) == 0 {
		b.morphError(ctx, uid, g, "Пришлите текст (≥ 10 символов) и/или фото-видео")
		return
	}

	if text == "" {
		// Медиа без текста — остаёмся на шаге: фото «склеиваются» (можно
		// докинуть ещё), «✓ Выбрано» обновляется, дальше — «Далее →».
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, "", uid, gg, fsm.StepPost)
		return
	}
	// Текст (с медиа или без) — сразу следующий шаг (мануал §3).
	b.passAndAdvance(ctx, uid, g, fsm.StepPost, "")
}

// handleDraftInput — текстовый ввод на шаге: валидный → сразу следующий шаг,
// невалидный → тот же шаг с «❌ …» (мануал §3).
func (b *Bot) handleDraftInput(ctx context.Context, uid int64, g store.Giveaway, text string) {
	if err := b.engine.HandleTextInput(ctx, g, text); err != nil {
		b.morphError(ctx, uid, g, err.Error())
		return
	}
	b.passAndAdvance(ctx, uid, g, g.DraftStep, "")
}

// handleChannelStepInput — шаг 2 текстом/форвардом: пересланный пост из
// канала или ссылка/ID → добавить канал и сразу выбрать его.
func (b *Bot) handleChannelStepInput(ctx context.Context, uid int64, g store.Giveaway, msg model.MessageUpdate) {
	if !b.tryAddChannel(ctx, uid, msg) {
		return // парсер уже объяснил ошибку (форвард без админства и т.п.)
	}
	// Добавлен — выбираем его каналом публикации и морфим шаг с «✓».
	channels, _ := b.db.ListChannels(ctx, uid)
	var chatID int64
	if msg.Link != nil {
		chatID = msg.Link.ChatID
	}
	for _, c := range channels {
		if c.ChatID == chatID {
			_ = b.db.UpdateGiveawayField(ctx, g.ID, "publish_channel_ids", []int64{c.ID})
			break
		}
	}
	gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
	b.morphStep(ctx, "", uid, gg, fsm.StepChannel)
}

// handleWizardFieldInput — ввод текста подэкрана (сообщение победителю /
// текст итогов): сохраняем и возвращаем шаг 9 (мануал §7).
func (b *Bot) handleWizardFieldInput(ctx context.Context, uid int64, text string) bool {
	sess, err := b.db.GetSessionPayload(ctx, uid)
	if err != nil || sess.State != store.SessionWizard {
		return false
	}
	field, _ := sess.Payload["field"].(string)
	if field == "" {
		return false
	}
	g, err := b.db.GetDraftGiveaway(ctx, uid)
	if err != nil {
		_ = b.db.ClearSession(ctx, uid)
		return true
	}
	title := "Сообщение победителю"
	if field == "results_post_text" {
		title = "Текст отдельного поста с итогами"
	}
	if strings.TrimSpace(text) == "" {
		_ = b.api.SendText(ctx, uid, "⚠️ Пустой текст не сохранён. Пришлите текст или нажмите «← К настройкам».")
		return true
	}
	if err := util.ValidateLongText(title, text); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ "+err.Error())
		return true
	}
	_ = b.db.UpdateGiveawayField(ctx, g.ID, field, text)
	_ = b.db.SetSessionPayload(ctx, uid, store.SessionWizard, map[string]any{})
	gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
	_ = b.db.SetDraftStep(ctx, g.ID, fsm.StepExtras)
	gg.DraftStep = fsm.StepExtras
	// Шаг 9 морфится на месте активного сообщения (это был подэкран ✉️).
	b.morphStep(ctx, "", uid, gg, fsm.StepExtras)
	return true
}

// ---------- выбор вариантов (fsm_opt) ----------

// selectPublishChannel — выбрать канал публикации (single-select) и зеркально
// включить его в каналы подписок (мануал §4 шаг 4: канал публикации предвыбран
// и не снимается).
func (b *Bot) selectPublishChannel(ctx context.Context, giveawayID, chID int64) {
	_ = b.db.UpdateGiveawayField(ctx, giveawayID, "publish_channel_ids", []int64{chID})
	g, err := b.db.GetGiveawayAny(ctx, giveawayID)
	if err != nil {
		return
	}
	for _, id := range g.SubscribeChannelIDs {
		if id == chID {
			return
		}
	}
	_ = b.db.UpdateGiveawayField(ctx, giveawayID, "subscribe_channel_ids",
		append(g.SubscribeChannelIDs, chID))
}

// wizardSelectOption — выбор варианта шага: пишет поле и морфит шаг («✓ »
// переезжает на выбранный вариант). key может быть с аргументом: "ch:12".
func (b *Bot) wizardSelectOption(ctx context.Context, uid int64, g store.Giveaway, key, cbID string) {
	keyParts := strings.SplitN(key, ":", 2)
	k := keyParts[0]
	arg := ""
	if len(keyParts) == 2 {
		arg = keyParts[1]
	}

	switch k {
	case "post_example":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "post_text", postExampleText)
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepPost)

	case "ch":
		chID, _ := strconv.ParseInt(arg, 10, 64)
		ch, err := b.db.GetChannel(ctx, chID, uid)
		if err != nil {
			b.api.AnswerCallback(ctx, cbID, "Канал не найден")
			return
		}
		if !ch.IsBotAdmin {
			b.api.AnswerCallback(ctx, cbID, "⚠️ Бот не админ в «"+channelLabel(ch)+"». Нажмите «➕ Подключить канал»")
			return
		}
		// Живая проверка админства (кэш мог протухнуть после смены токена).
		live, lerr := b.api.IsBotAdminInChat(ctx, ch.ChatID)
		if lerr != nil || !live {
			_ = b.db.SetChannelAdminFlag(ctx, ch.ID, false)
			b.api.AnswerCallback(ctx, cbID, "⚠️ Проверка: бот больше не админ в «"+channelLabel(ch)+"». Передобавьте канал")
			gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
			b.morphStep(ctx, "", uid, gg, fsm.StepChannel)
			return
		}
		b.selectPublishChannel(ctx, g.ID, chID)
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepChannel)

	case "locked":
		b.api.AnswerCallback(ctx, cbID, "🔒 Канал публикации всегда включён в подписки")

	case "name":
		variants := b.nameVariants(ctx, g)
		i, _ := strconv.Atoi(arg)
		if i < 0 || i >= len(variants) {
			return
		}
		if err := util.ValidateName(variants[i]); err != nil {
			b.api.AnswerCallback(ctx, cbID, "⚠️ "+err.Error())
			return
		}
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "name", variants[i])
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepName)

	case "btn":
		variants := []string{"Участвовать", "Участвую!", "Хочу приз", "Мне повезёт"}
		i, _ := strconv.Atoi(arg)
		if i < 0 || i >= len(variants) {
			return
		}
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "button_text", variants[i])
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepButton)

	case "win":
		n, _ := strconv.Atoi(arg)
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "winners_count", n)
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepWinners)

	case "fin":
		for _, opt := range b.finishQuickOptions() {
			if opt.key == arg {
				_ = b.db.UpdateGiveawayField(ctx, g.ID, "finish_mode", "time")
				_ = b.db.UpdateGiveawayField(ctx, g.ID, "finish_at", opt.t)
				break
			}
		}
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepFinish)

	case "fin_cal":
		// Календарь — подэкран шага 7 (морфим то же сообщение).
		text := fsm.StepHeader(fsm.StepFinish) + "\n**Выберите день итогов**\n_(календарь, МСК)_"
		if cbID != "" {
			b.api.AnswerCallbackEdit(ctx, cbID, text, util.CalMonthKeyboard(0))
		}
	case "fin_count":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "finish_mode", "count")
		if g.FinishParticipantsCount == 0 {
			_ = b.db.UpdateGiveawayField(ctx, g.ID, "finish_participants_count", 100)
		}
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepFinish)
	case "fin_time":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "finish_mode", "time")
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepFinish)

	case "start":
		for _, opt := range b.startOptions() {
			if opt.key != arg {
				continue
			}
			if opt.key == "now" {
				_ = b.db.UpdateGiveawayField(ctx, g.ID, "publish_now", true)
			} else {
				_ = b.db.UpdateGiveawayField(ctx, g.ID, "publish_now", false)
				_ = b.db.UpdateGiveawayField(ctx, g.ID, "publish_at", opt.t)
			}
			break
		}
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepStart)

	case "cap":
		mode := store.CaptchaNone
		if g.CaptchaMode != store.CaptchaAll {
			mode = store.CaptchaAll
		}
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "captcha_mode", mode)
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepExtras)

	case "fr":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "referral_enabled", !g.ReferralEnabled)
		if !g.ReferralEnabled && g.ReferralLimit == 0 {
			_ = b.db.UpdateGiveawayField(ctx, g.ID, "referral_limit", 10)
		}
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepExtras)

	case "frlim":
		n, _ := strconv.Atoi(arg)
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "referral_limit", n)
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepExtras)

	case "msg":
		// Подэкран «✉️ Сообщение победителю» (мануал §7): морфим шаг в экран
		// ввода; текст пишет handleWizardFieldInput.
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionWizard, map[string]any{"field": "winners_message"})
		text := "**✉️ Сообщение победителю**\nБот сам отправит этот текст каждому победителю в личные сообщения сразу после подведения итогов. Напишите, к кому обратиться за призом, в какой срок и что прислать.\n\nНапример (нажмите, чтобы скопировать):\n`Поздравляем с победой! Напишите @manager_shop в течение 48 часов.`\n\n✍️ Отправьте текст сообщением — после отправки вернёмся к настройкам."
		kb := model.NewKeyboard()
		kb.AddRow().AddCallBack("🗑 Без сообщения", util.CBFSMOption+"msg_clear")
		kb.AddRow().AddCallBack("← К настройкам", util.CBFSMOption+"msg_back")
		if cbID != "" {
			b.api.AnswerCallbackEdit(ctx, cbID, text, kb)
		}

	case "msg_clear":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "winners_message", "")
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionWizard, map[string]any{})
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepExtras)

	case "msg_back":
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionWizard, map[string]any{})
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepExtras)

	case "resloc":
		// Переключение места итогов; «отдельным постом» без текста — просим
		// текст подэкраном сразу (мануал: текст запрашивается у создателя).
		newVal := !g.ResultsInOriginalPost
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "results_in_original_post", newVal)
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		if !newVal && gg.ResultsPostText == "" {
			_ = b.db.SetSessionPayload(ctx, uid, store.SessionWizard, map[string]any{"field": "results_post_text"})
			text := "**📄 Текст поста с итогами**\nИтоги выйдут отдельным постом. Отправьте его текст (заголовок и поздравление — победители будут дописаны автоматически).\n\n✍️ Отправьте текст сообщением — после сохранения вернёмся к настройкам."
			kb := model.NewKeyboard()
			kb.AddRow().AddCallBack("⏭ Стандартный заголовок", util.CBFSMOption+"res_std")
			kb.AddRow().AddCallBack("← К настройкам", util.CBFSMOption+"msg_back")
			if cbID != "" {
				b.api.AnswerCallbackEdit(ctx, cbID, text, kb)
			}
			return
		}
		b.morphStep(ctx, cbID, uid, gg, fsm.StepExtras)

	case "res_std":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "results_post_text", "🏁 Итоги розыгрыша")
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionWizard, map[string]any{})
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepExtras)

	case "check_connect":
		// Живая проверка НЕВОЗМОЖНА для неизвестных каналов (GET /chats удалён
		// из API): новые каналы прилетают событием bot_added — оно уже
		// обработано (onBotAddedConnectChannel). До события — честный ответ.
		b.api.AnswerCallback(ctx, cbID, "Пока не вижу новых каналов. Проверьте оба права и перешлите пост из канала")

	case "back_to":
		step := fsm.NormalizeStep(arg)
		_ = b.db.ClearSession(ctx, uid)
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		_ = b.db.SetDraftStep(ctx, g.ID, step)
		gg.DraftStep = step
		b.api.AnswerCallback(ctx, cbID, "Возвращаюсь к шагу "+strconv.Itoa(fsm.StepNumber(step)))
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", nil)
		b.showStep(ctx, uid, gg, step)

	case "noop":
		b.api.AnswerCallback(ctx, cbID, "")
	}
}

// tryAddChannel — парсит сообщение как канал (форвард/ссылка/ID), проверяет
// админство и сохраняет. true = канал добавлен. Ошибки объясняет сообщением.
func (b *Bot) tryAddChannel(ctx context.Context, uid int64, msg model.MessageUpdate) bool {
	var chatID int64
	if msg.Link != nil && msg.Link.ChatID != 0 {
		chatID = msg.Link.ChatID
	} else {
		text := strings.TrimSpace(msg.Body.Text)
		if isChannelLink(text) {
			chat, err := b.api.GetChatByLink(ctx, text)
			if err != nil {
				_ = b.api.SendText(ctx, uid, "⚠️ Канал по ссылке не найден. Проверьте ссылку (канал должен быть публичным).")
				return false
			}
			chatID = chat.ChatID
		} else {
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				_ = b.api.SendText(ctx, uid, "⚠️ Не похоже на канал. Перешлите пост из канала или отправьте ссылку/ID.")
				return false
			}
			chatID = n
		}
	}
	if chatID == 0 {
		_ = b.api.SendText(ctx, uid, "⚠️ Не похоже на канал. Перешлите пост из канала или отправьте ссылку/ID.")
		return false
	}
	info, isAdmin, err := b.api.CheckChannelAdmin(ctx, chatID)
	if err != nil {
		b.log.Warn("add channel: check admin", "err", err, "chat", chatID, "user", uid)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось получить доступ к каналу. Убедитесь, что бот добавлен админом, и повторите.")
		return false
	}
	if !isAdmin {
		b.showConnectChannelScreen(ctx, uid, "")
		return false
	}
	if _, err := b.db.AddChannel(ctx, store.Channel{
		OwnerUserID: uid, ChatID: chatID, Title: info.Title, Link: info.Link,
		IsPublic: info.IsPublic, IsBotAdmin: true,
	}); err != nil {
		b.log.Debug("add channel: save (уже добавлен?)", "err", err, "chat", chatID, "user", uid)
	}
	_ = b.api.SendText(ctx, uid, "✅ Канал «"+ifEmpty(info.Title, fmt.Sprintf("#%d", chatID))+"» подключён.")
	return true
}

// showConnectChannelScreen — экран подключения канала по мануалу §5:
// картинка со схемой прав + инструкция + «🔄 Проверить подключение».
// returnStep — шаг мастера, куда вернуться (пусто = из «Мои каналы»).
func (b *Bot) showConnectChannelScreen(ctx context.Context, uid int64, returnStep string) {
	_ = b.api.SendImage(ctx, uid, adminRightsPNG, "Какие права включить боту")
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("🔄 Проверить подключение", util.CBFSMOption+"check_connect")
	if returnStep != "" {
		kb.AddRow().AddCallBack("← К шагу "+strconv.Itoa(fsm.StepNumber(returnStep)), util.CBFSMOption+"back_to:"+returnStep)
	} else {
		kb.AddRow().AddCallBack("← В мои каналы", util.CBChannelBack)
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"➕ **Подключение канала**\n\n"+
			"**Способ 1 — сделать бота админом**\n"+
			"1. Откройте канал → **Администраторы** → **Добавить**.\n"+
			"2. Найдите бота по имени, добавьте.\n"+
			"3. Включите «Читать все сообщения» и «Публиковать, редактировать и удалять сообщения», сохраните.\n\n"+
			"**Способ 2** — если бот уже админ, перешлите сюда любой пост из канала.\n\n"+
			"⏳ Жду подключения — как только бот станет админом, нажмите «🔄 Проверить».", kb)
	// Помним, куда возвращаться после успешного подключения.
	_ = b.db.SetSessionPayload(ctx, uid, store.SessionAddingChannel, map[string]any{"return_step": returnStep})
}

// ---------- роутер callback'ов мастера ----------

// handleWizardCallback — все payload мастера (fsm_*, cal:*, тогглы каналов
// подписок). Снимает часики с кнопки сам (морфинг/уведомление), поэтому
// onMessageCallback не должен отвечать за него повторно.
func (b *Bot) handleWizardCallback(ctx context.Context, uid int64, p, cbID string) {
	// Тогглы каналов подписок (шаг 4) — морфингом шага.
	if strings.HasPrefix(p, util.CBChannelToggleSub) {
		b.wizardToggleSub(ctx, uid, p, cbID)
		return
	}
	// Календарь итогов (подэкран шага 7).
	if strings.HasPrefix(p, util.CBCal) {
		b.handleCalendar(ctx, uid, strings.TrimPrefix(p, util.CBCal), cbID)
		return
	}

	g, err := b.db.GetDraftGiveaway(ctx, uid)
	if err != nil {
		b.api.AnswerCallback(ctx, cbID, "Создание не активно. Начните заново: 🎁 Создать розыгрыш")
		return
	}

	switch {
	case p == util.CBFSMNext:
		if !b.engine.StepComplete(ctx, g, g.DraftStep) {
			b.api.AnswerCallback(ctx, cbID, fsm.MissingText(g.DraftStep))
			return
		}
		b.passAndAdvance(ctx, uid, g, g.DraftStep, cbID)

	case p == util.CBFSMBack:
		b.goBack(ctx, uid, g, cbID)

	case p == util.CBFSMCancel:
		// Экран подтверждения отмены (мануал §3): всё сохранится в черновик.
		b.api.AnswerCallbackEdit(ctx, cbID,
			"⏸ **Остановить создание?**\nВсё сохранится в черновик — продолжите из «Мои розыгрыши → Черновики».",
			util.CancelConfirmKeyboard())

	case p == util.CBFSMExitYes:
		_ = b.engine.Abandon(ctx, g.ID)
		if g.Name != "" {
			b.api.AnswerCallbackEdit(ctx, cbID, "✖ Создание остановлено. Черновик — в «Мои розыгрыши → Черновики».", nil)
			b.sendMainMenu(ctx, uid, "Черновик сохранён.")
		} else {
			_ = b.db.DeleteGiveaway(ctx, g.ID, uid)
			b.api.AnswerCallbackEdit(ctx, cbID, "✖ Создание отменено.", nil)
			b.sendMainMenu(ctx, uid, "Главное меню:")
		}

	case p == util.CBFSMContinue:
		// «▶ Продолжить» после паузы — вернуть текущий шаг на место.
		b.morphStep(ctx, cbID, uid, g, g.DraftStep)

	case p == util.CBFSMHint:
		// Только notification (≤150 символов), сообщение не меняется (§3).
		b.api.AnswerCallback(ctx, cbID, fsm.HintForStep(g.DraftStep))

	case p == util.CBFSMConfirm:
		b.wizardConfirm(ctx, uid, g, cbID)

	case p == util.CBFSMSave:
		_ = b.engine.Abandon(ctx, g.ID)
		b.api.AnswerCallbackEdit(ctx, cbID, "💾 Сохранено в черновики.", nil)
		b.sendMainMenu(ctx, uid, "Черновик — в «Мои розыгрыши → Черновики».")

	case strings.HasPrefix(p, util.CBFSMEdit):
		// Правка пункта с экрана проверки: к шагу, после «Далее» — назад на
		// проверку (мануал §4 шаг 10).
		step := fsm.NormalizeStep(strings.TrimPrefix(p, util.CBFSMEdit))
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionWizard, map[string]any{"return_to": fsm.StepCheck})
		_ = b.db.SetDraftStep(ctx, g.ID, step)
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.api.AnswerCallback(ctx, cbID, "Правка: "+fsm.StepTitle(step))
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", nil)
		b.showStep(ctx, uid, gg, step)

	case strings.HasPrefix(p, util.CBFSMOption):
		key := strings.TrimPrefix(p, util.CBFSMOption)
		b.wizardSelectOption(ctx, uid, g, key, cbID)
	}
}

// wizardConfirm — «🚀 Опубликовать»: финальная валидация → публикация.
func (b *Bot) wizardConfirm(ctx context.Context, uid int64, g store.Giveaway, cbID string) {
	if err := b.engine.Confirm(ctx, g.ID); err != nil {
		var ve *fsm.ValidationError
		msg := "⚠️ Не удалось опубликовать. Попробуйте позже."
		if e, ok := err.(*fsm.ValidationError); ok {
			ve = e
			msg = "⚠️ " + ve.Msg
		} else {
			b.log.Warn("confirm", "err", err, "id", g.ID, "user", uid)
		}
		b.api.AnswerCallback(ctx, cbID, msg)
		return
	}
	b.api.AnswerCallback(ctx, cbID, "🚀 Публикую…")
	_ = b.db.ClearSession(ctx, uid)
	// «Сразу»: публикация немедленно, без ожидания тика планировщика.
	published := false
	if b.publishNow != nil {
		if gg, err := b.db.GetGiveawayAny(ctx, g.ID); err == nil && gg.PublishNow {
			b.publishNow(ctx, gg.ID)
			published = true
		}
	}
	if published {
		b.sendMainMenu(ctx, uid, "🚀 Розыгрыш опубликован! Пост уже в каналах, кнопка «Участвовать» активна.")
	} else {
		b.sendMainMenu(ctx, uid, "✅ Розыгрыш запланирован! Пост выйдет автоматически в назначенное время («Мои розыгрыши → Запланированные»).")
	}
}

// wizardToggleSub — тоггл канала подписки на шаге 4 с морфингом шага
// (✓/⬜ перерисовываются на месте). Не-админ каналы блокируются.
func (b *Bot) wizardToggleSub(ctx context.Context, uid int64, p, cbID string) {
	g, err := b.db.GetDraftGiveaway(ctx, uid)
	if err != nil {
		b.api.AnswerCallback(ctx, cbID, "Создание не активно.")
		return
	}
	idStr := strings.TrimPrefix(p, util.CBChannelToggleSub)
	chID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return
	}
	// Канал публикации предвыбран и не снимается.
	for _, pid := range g.PublishChannelIDs {
		if pid == chID {
			b.api.AnswerCallback(ctx, cbID, "🔒 Канал публикации всегда включён в подписки")
			return
		}
	}
	if ch, err := b.db.GetChannel(ctx, chID, uid); err == nil && !ch.IsBotAdmin {
		b.api.AnswerCallback(ctx, cbID, "⚠️ Бот не админ в «"+channelLabel(ch)+"» — выбрать нельзя")
		return
	}
	found := false
	var updated []int64
	for _, id := range g.SubscribeChannelIDs {
		if id == chID {
			found = true
			continue
		}
		updated = append(updated, id)
	}
	if !found {
		updated = append(updated, chID)
	}
	if err := b.db.UpdateGiveawayField(ctx, g.ID, "subscribe_channel_ids", updated); err != nil {
		b.api.AnswerCallback(ctx, cbID, "⚠️ Не удалось обновить выбор")
		return
	}
	gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
	b.morphStep(ctx, cbID, uid, gg, fsm.StepSubs)
}

// handleCalendar — навигация календаря итогов (мануал §6): m — месяц,
// d — день (экран времени), t — дата+время (запись finish_at), back — к
// быстрым вариантам. Все переходы морфят сообщение.
func (b *Bot) handleCalendar(ctx context.Context, uid int64, arg, cbID string) {
	parts := strings.SplitN(arg, ":", 2)
	kind := parts[0]
	val := ""
	if len(parts) == 2 {
		val = parts[1]
	}
	g, err := b.db.GetDraftGiveaway(ctx, uid)
	if err != nil {
		b.api.AnswerCallback(ctx, cbID, "Создание не активно.")
		return
	}

	morph := func(text string, kb *model.Keyboard) {
		if cbID != "" {
			b.api.AnswerCallbackEdit(ctx, cbID, text, kb)
		}
	}

	switch kind {
	case "m":
		off, _ := strconv.Atoi(val)
		if off < 0 {
			off = 0
		}
		if off > 12 {
			off = 12
		}
		morph(fsm.StepHeader(fsm.StepFinish)+"\n**Выберите день итогов**\n_(календарь, МСК)_", util.CalMonthKeyboard(off))
	case "d":
		day, err := time.ParseInLocation("2006-01-02", val, util.MoscowLocation())
		if err != nil {
			b.api.AnswerCallback(ctx, cbID, " ")
			return
		}
		morph(fsm.StepHeader(fsm.StepFinish)+"\n**День: "+day.Format("Пн, 2 января 2006")+"**\nВыберите время (МСК) или пришлите своё сообщением в формате ЧЧ:ММ.",
			util.CalTimeKeyboard(val))
	case "t":
		// cal:t:2006-01-02T15:04
		t, err := time.ParseInLocation("2006-01-02T15:04", val, util.MoscowLocation())
		if err != nil {
			b.api.AnswerCallback(ctx, cbID, " ")
			return
		}
		if t.Before(time.Now().Add(10 * time.Minute)) {
			b.api.AnswerCallback(ctx, cbID, "Этот день уже прошёл — выберите будущий")
			return
		}
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "finish_mode", "time")
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "finish_at", t.UTC())
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepFinish)
	case "back":
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.morphStep(ctx, cbID, uid, gg, fsm.StepFinish)
	default:
		// noop/past — пустой ответ (каждое нажатие обязано отвечать).
		switch kind {
		case "past":
			b.api.AnswerCallback(ctx, cbID, "Этот день уже прошёл")
		default:
			b.api.AnswerCallback(ctx, cbID, " ")
		}
	}
}

// onBotAddedConnectChannel — бота сделали админом в канале, пока юзер на
// экране подключения (мануал §5.3): привязываем канал к организатору и
// возвращаем к шагу мастера, откуда пришли.
func (b *Bot) onBotAddedConnectChannel(ctx context.Context, uid, chatID int64) {
	sess, err := b.db.GetSessionPayload(ctx, uid)
	if err != nil || sess.State != store.SessionAddingChannel {
		return
	}
	returnStep, _ := sess.Payload["return_step"].(string)

	info, isAdmin, err := b.api.CheckChannelAdmin(ctx, chatID)
	if err != nil || !isAdmin {
		b.log.Debug("bot_added connect: not admin", "chat", chatID, "err", err)
		return
	}
	if _, err := b.db.AddChannel(ctx, store.Channel{
		OwnerUserID: uid, ChatID: chatID, Title: info.Title, Link: info.Link,
		IsPublic: info.IsPublic, IsBotAdmin: true,
	}); err != nil {
		b.log.Debug("bot_added connect: save", "err", err, "chat", chatID)
	}
	title := ifEmpty(info.Title, fmt.Sprintf("канал #%d", chatID))
	_ = b.api.SendText(ctx, uid, "✅ Канал «"+title+"» подключён!")
	_ = b.db.ClearSession(ctx, uid)

	// Возврат к шагу мастера (если подключали из него; из «Мои каналы» —
	// return_step пуст → список каналов).
	if returnStep != "" {
		step := fsm.NormalizeStep(returnStep)
		if g, err := b.db.GetDraftGiveaway(ctx, uid); err == nil {
			_ = b.db.SetDraftStep(ctx, g.ID, step)
			if step == fsm.StepChannel {
				// Сразу выбираем новый канал каналом публикации.
				chs, _ := b.db.ListChannels(ctx, uid)
				for _, c := range chs {
					if c.ChatID == chatID {
						b.selectPublishChannel(ctx, g.ID, c.ID)
						break
					}
				}
			}
			gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
			_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", nil)
			b.showStep(ctx, uid, gg, step)
			return
		}
	}
	b.showChannels(ctx, uid)
}

// mergeMediaTokens объединяет существующие и новые медиа-токены без дублей.
func mergeMediaTokens(existing, incoming []store.MediaToken) ([]store.MediaToken, int) {	seen := make(map[string]bool, len(existing))
	all := make([]store.MediaToken, 0, len(existing)+len(incoming))
	for _, m := range existing {
		if m.Token != "" && !seen[m.Token] {
			seen[m.Token] = true
			all = append(all, m)
		}
	}
	added := 0
	for _, m := range incoming {
		if m.Token != "" && !seen[m.Token] {
			seen[m.Token] = true
			all = append(all, m)
			added++
		}
	}
	return all, added
}
