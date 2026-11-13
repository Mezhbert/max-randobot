package handlers

// Трекинг-ссылки: чат-флоу по ревизии 13.11 (Вова Лунёв).
//
//	Трекинг → [Конкурс | Реклама]
//	Реклама:  канал (radio) → название → стоимость (пропуск) →
//	          приватный: «Уникальная ссылка»
//	          публичный: «Уникальная ссылка» | «Суперссылка» → пост
//	Конкурс:  каналы (мультивыбор) → название → «Конкурсная ссылка(и)» —
//	          генерируются СРАЗУ ДЛЯ ВСЕХ выбранных каналов.
//
// На каждом шаге со второго — «← Назад» (без начала заново), на пикере
// каналов — «➕ Подключить канал». Детальная аналитика — в миниаппе
// (analytics.html), в чате только сводки и CSV.

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/maxbot/giveawaybot/internal/fsm"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// onUserAdded — юзер вступил в канал, где бот админ.
// Атрибуция UTM (если юзер пришёл по UTM-ссылке) + лог подписки + подписчик
// ссылки (retention-таблица, миграция 0014).
func (b *Bot) onUserAdded(ctx context.Context, u model.Update) {
	userID := u.UserID
	chatID := u.ChatID
	if userID == 0 || chatID == 0 {
		return
	}
	usr := u.GetUser()
	if usr.UserID != 0 {
		_ = b.db.UpsertUser(ctx, store.User{
			UserID:    usr.UserID,
			FirstName: usr.FirstName,
			Username:  usr.Username,
			IsBot:     usr.IsBot,
		})
	}
	link, attributed, err := b.db.UTMAccountSignup(ctx, userID, chatID)
	if err != nil {
		b.log.Debug("utm account signup", "err", err)
	}
	if attributed {
		if err := b.db.UpsertUTMSubscriber(ctx, link.ID, userID, chatID); err != nil {
			b.log.Debug("utm subscriber upsert", "err", err, "link", link.ID, "user", userID)
		}
	}
	source := "post"
	if attributed {
		source = "utm"
	}
	_ = b.db.LogChannelJoin(ctx, userID, chatID, source)
}

// onUserRemoved — юзер покинул канал. Фиксируем отписку везде.
func (b *Bot) onUserRemoved(ctx context.Context, u model.Update) {
	userID := u.UserID
	chatID := u.ChatID
	if userID == 0 || chatID == 0 {
		return
	}
	_ = b.db.MarkUnsubscribedByChat(ctx, userID, chatID)
	_ = b.db.UTMAccountUnsub(ctx, userID, chatID)
	_ = b.db.MarkUTMUnsubscribedByUser(ctx, userID, chatID)
}

// onBotStartedPayload — легаси-диплинк utm_<code> (старые ссылки).
func (b *Bot) onBotStartedPayload(ctx context.Context, userID int64, payload string) {
	if payload == "" {
		return
	}
	b.log.Info("deeplink start", "user", userID, "payload", payload)
	if strings.HasPrefix(payload, "part_") {
		idStr := strings.TrimPrefix(payload, "part_")
		b.onParticipate(ctx, userID, idStr, "")
		return
	}
	if strings.HasPrefix(payload, "ref_") {
		code := strings.TrimPrefix(payload, "ref_")
		if link, err := b.db.GetReferralLinkByCode(ctx, code); err == nil {
			if err := b.db.SaveReferralInvite(ctx, link.GiveawayID, link.UserID, userID); err != nil {
				b.log.Debug("referral invite", "err", err, "code", code)
			} else {
				b.log.Info("referral invite", "giveaway", link.GiveawayID, "inviter", link.UserID, "invitee", userID)
			}
		}
		b.sendStartScreen(ctx, userID)
		return
	}
	code := strings.TrimPrefix(payload, "utm_")
	if code == "" || code == payload {
		b.sendStartScreen(ctx, userID)
		return
	}
	link, err := b.db.GetUTMLinkByCode(ctx, code)
	if err != nil {
		b.log.Debug("utm link not found", "code", code, "err", err)
		b.sendStartScreen(ctx, userID)
		return
	}
	if err := b.db.UTMRecordClick(ctx, link.ID, userID); err != nil {
		b.log.Warn("utm record click", "err", err)
	}
	if link.ChannelID != 0 {
		b.sendUTMChannelScreen(ctx, userID, link)
		return
	}
	b.sendStartScreen(ctx, userID)
}

// sendUTMChannelScreen — экран для посетителя, пришедшего по легаси-диплинку.
func (b *Bot) sendUTMChannelScreen(ctx context.Context, userID int64, link store.UTMLink) {
	ch, err := b.db.GetChannel(ctx, link.ChannelID, link.OwnerUserID)
	if err != nil {
		b.sendStartScreen(ctx, userID)
		return
	}
	title := ifEmpty(ch.Title, fmt.Sprintf("канал #%d", ch.ChatID))
	channelURL := util.ChannelURL(ch.Link)
	postURL := ""
	if ref := strings.TrimSpace(link.PostRef); util.IsHTTPURL(ref) {
		postURL = ref
	}
	if channelURL != "" || postURL != "" {
		action := "Откройте канал и подпишитесь — кнопка ниже 👇"
		if postURL != "" {
			action = "Откройте пост и подпишитесь на канал — кнопки ниже 👇"
		}
		_, _ = b.api.SendTextWithKeyboard(ctx, userID,
			fmt.Sprintf("👋 Привет!\n\nВы пришли по ссылке на канал «%s».\n%s", title, action),
			util.ChannelEntryKeyboard(title, channelURL, postURL))
		return
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, userID,
		fmt.Sprintf("👋 Привет!\n\nВы пришли по ссылке на канал «%s».\n"+
			"Найдите его в поиске MAX по названию и подпишитесь.", title),
		util.BackKeyboard())
}

// ---------- флоу создания (сессия SessionUTMCreate) ----------

// startQuickUTM — вход «Трекинг»: стартовый шаг выбора механики.
func (b *Bot) startQuickUTM(ctx context.Context, uid int64) {
	_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, map[string]any{"stage": "mode"})
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("🏁 Конкурс", util.CBUTMMode+"contest").
		AddCallBack("📣 Реклама", util.CBUTMMode+"ads")
	kb.AddRow().AddCallBack("🏠 В меню", util.CBMainMenu)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"🔗 Трекинг\n\nВы можете вести трекинг для запуска конкурсов и классической "+
			"рекламы. Выберите вашу механику:", kb)
}

// utmShowChannelPicker — шаг выбора канала (Реклама, radio) + Подключить + Назад.
func (b *Bot) utmShowChannelPicker(ctx context.Context, uid int64, sess store.UserSession) {
	channels, err := b.db.ListChannels(ctx, uid)
	if err != nil || len(channels) == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"🔗 Трекинг\n\nСначала добавьте канал — куда должна вести ссылка.",
			util.CancelKeyboard())
		return
	}
	var admin []store.Channel
	for _, c := range channels {
		if c.IsBotAdmin {
			admin = append(admin, c)
		}
	}
	if len(admin) == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"🔗 Трекинг\n\nНет каналов, где бот админ. Сначала добавьте бота админом в канал.",
			util.CancelKeyboard())
		return
	}
	sel := utmSessChannel(sess)
	kb := model.NewKeyboard()
	for _, c := range admin {
		mark := "○ "
		if sel == c.ID {
			mark = "✓ "
		}
		kb.AddRow().AddCallBack(mark+channelLabel(c), util.CBUTMQuickChSel+strconv.FormatInt(c.ID, 10))
	}
	kb.AddRow().AddCallBack("➕ Подключить канал", util.CBChannelAdd)
	kb.AddRow().AddCallBack("← Назад", util.CBUTMBack)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"🔗 Шаг 2 · Канал\n\nВыберите канал, на который будет вести ссылка:", kb)
}

// utmShowContestPicker — шаг выбора каналов (Конкурс, мультивыбор).
func (b *Bot) utmShowContestPicker(ctx context.Context, uid int64, sess store.UserSession) {
	channels, _ := b.db.ListChannels(ctx, uid)
	sel := utmSessChannels(sess)
	kb := model.NewKeyboard()
	n := 0
	for _, c := range channels {
		if !c.IsBotAdmin {
			continue
		}
		mark := "⬜ "
		if sel[c.ID] {
			mark = "✅ "
			n++
		}
		kb.AddRow().AddCallBack(mark+channelLabel(c), util.CBUTMToggle+strconv.FormatInt(c.ID, 10))
	}
	if n > 0 {
		word := "ссылку"
		if n > 1 {
			word = "ссылки"
		}
		kb.AddRow().AddCallBack(fmt.Sprintf("Далее · %d канал(ов) → %s", n, word), util.CBUTMReady)
	}
	kb.AddRow().AddCallBack("➕ Подключить канал", util.CBChannelAdd)
	kb.AddRow().AddCallBack("← Назад", util.CBUTMBack)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"🔗 Шаг · Каналы конкурса\n\nОтметьте каналы, под которые создать ссылки "+
			"(можно сразу несколько — под каждый будет своя ссылка):", kb)
}

// utmAskName — шаг названия.
func (b *Bot) utmAskName(ctx context.Context, uid int64) {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("← Назад", util.CBUTMBack)
	kb.AddRow().AddCallBack("✖ Отмена", util.CBMainMenu)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"🔗 Шаг 3 · Название\n\nОтправьте НАЗВАНИЕ ссылки — как её искать в статистике.\n"+
			"Например: «Telegram», «Реклама у Иванова», «Сторис 15.11»", kb)
}

// utmAskCost — шаг стоимости (только Реклама): сумма или «Пропустить».
func (b *Bot) utmAskCost(ctx context.Context, uid int64) {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("⏭ Пропустить", util.CBUTMSkipCost)
	kb.AddRow().AddCallBack("← Назад", util.CBUTMBack)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"🔗 Шаг 4 · Стоимость размещения\n\nУкажите сумму размещения в рублях — "+
			"будем автоматически считать цену за подписчика (грязную и чистую).\n"+
			"Введите сумму сообщением или нажмите «⏭ Пропустить».", kb)
}

// utmAskLinkType — ветвление по типу канала (публичный/приватный).
func (b *Bot) utmAskLinkType(ctx context.Context, uid int64, sess store.UserSession) {
	chID := utmSessChannel(sess)
	ch, err := b.db.GetChannel(ctx, chID, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Канал не найден, начните заново.")
		_ = b.db.ClearSession(ctx, uid)
		return
	}
	kb := model.NewKeyboard()
	if ch.IsPublic {
		kb.AddRow().AddCallBack("🔗 Уникальная ссылка", util.CBUTMType+"unique")
		kb.AddRow().AddCallBack("⚡ Суперссылка", util.CBUTMType+"super")
		kb.AddRow().AddCallBack("← Назад", util.CBUTMBack)
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"🔗 Шаг 5 · Тип ссылки\n\nДля публичного канала вы можете создать:\n"+
				"• прямую уникальную ссылку — пользователь будет переходить на последний пост канала;\n"+
				"• суперссылку — пользователь будет приходить на конкретный пост, который вы выберете.",
			kb)
		return
	}
	kb.AddRow().AddCallBack("🔗 Уникальная ссылка", util.CBUTMType+"unique")
	kb.AddRow().AddCallBack("← Назад", util.CBUTMBack)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"🔗 Шаг 5 · Тип ссылки\n\nДля приватного канала вы можете создать только прямую "+
			"уникальную ссылку. Пользователь будет переходить на последний пост из канала "+
			"либо подавать заявку, если у вас включены заявки.\n\n"+
			"Чтобы сгенерировать ссылку, нажмите «🔗 Уникальная ссылка».", kb)
}

// utmAskPost — шаг суперссылки: ссылка на конкретный пост.
func (b *Bot) utmAskPost(ctx context.Context, uid int64, sess store.UserSession) {
	title := ""
	if chID := utmSessChannel(sess); chID != 0 {
		if ch, err := b.db.GetChannel(ctx, chID, uid); err == nil {
			title = ifEmpty(ch.Title, "")
		}
	}
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("← Назад", util.CBUTMBack)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"⚡ Шаг · Пост-источник\n\nСсылка будет вести на конкретный пост, который вы выберете.\n"+
			"Отправьте в сообщении ссылку поста из вашего канала «"+title+"» "+
			"по типу https://max.ru/channel/post", kb)
}

// utmAskContestGenerate — конкурс: генерация ссылок для всех каналов.
func (b *Bot) utmAskContestGenerate(ctx context.Context, uid int64, sess store.UserSession) {
	ids := utmSessChannelList(sess)
	if len(ids) == 0 {
		b.utmBack(ctx, uid)
		return
	}
	word := "Конкурсная ссылка"
	if len(ids) > 1 {
		word = "Конкурсные ссылки"
	}
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("🏁 " + word, util.CBUTMGenContest)
	kb.AddRow().AddCallBack("← Назад", util.CBUTMBack)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("🏁 Шаг · Генерация\n\nДля конкурсов создаются прямые уникальные ссылки "+
			"(по одной на каждый выбранный канал, всего: %d). Пользователь будет переходить на "+
			"последний пост канала.\n\nЧтобы сгенерировать, нажмите «%s».", len(ids), word), kb)
}

// utmBack — «← Назад» на предыдущий шаг флоу.
func (b *Bot) utmBack(ctx context.Context, uid int64) {
	sess, err := b.db.GetSessionPayload(ctx, uid)
	if err != nil || sess.State != store.SessionUTMCreate {
		b.startQuickUTM(ctx, uid)
		return
	}
	mode, _ := sess.Payload["mode"].(string)
	stage, _ := sess.Payload["stage"].(string)

	prevStage := map[string]string{
		"mode":      "",
		"channel":   "mode",
		"channels":  "mode",
		"name":      "channel",
		"cost":      "name",
		"linktype":  "cost",
		"post":      "linktype",
		"gen":       "name",
	}[stage]
	if mode == "contest" {
		switch stage {
		case "name":
			prevStage = "channels"
		case "gen":
			prevStage = "name"
		}
	}
	if prevStage == "" {
		b.startQuickUTM(ctx, uid)
		return
	}
	payload := map[string]any{"stage": prevStage}
	for k, v := range sess.Payload {
		if k != "stage" {
			payload[k] = v
		}
	}
	_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload)
	switch prevStage {
	case "mode":
		b.startQuickUTM(ctx, uid)
	case "channel":
		b.utmShowChannelPicker(ctx, uid, sess)
	case "channels":
		b.utmShowContestPicker(ctx, uid, sess)
	case "name":
		b.utmAskName(ctx, uid)
	case "cost":
		b.utmAskCost(ctx, uid)
	case "linktype":
		b.utmAskLinkType(ctx, uid, sess)
	case "post":
		b.utmAskPost(ctx, uid, sess)
	case "gen":
		b.utmAskContestGenerate(ctx, uid, sess)
	}
}

// handleUTMCreateInput — текстовый ввод в флоу (название/стоимость/пост).
func (b *Bot) handleUTMCreateInput(ctx context.Context, uid int64, text string) bool {
	sess, err := b.db.GetSessionPayload(ctx, uid)
	if err != nil || sess.State != store.SessionUTMCreate {
		_ = b.db.ClearSession(ctx, uid)
		return false
	}
	stage, _ := sess.Payload["stage"].(string)
	switch stage {
	case "", "mode", "channel", "channels", "linktype", "gen":
		return false // выбор кнопками
	case "name":
		source := strings.TrimSpace(text)
		if err := util.ValidateUTMSource(source); err != nil {
			_ = b.api.SendText(ctx, uid, "⚠️ "+err.Error())
			return true
		}
		payload := map[string]any{"stage": "cost", "source": source}
		for k, v := range sess.Payload {
			if k != "stage" && k != "source" {
				payload[k] = v
			}
		}
		// Конкурс: стоимость не нужна — сразу генерация.
		if m, _ := payload["mode"].(string); m == "contest" {
			payload["stage"] = "gen"
			_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload)
			ns, _ := b.db.GetSessionPayload(ctx, uid)
			b.utmAskContestGenerate(ctx, uid, ns)
			return true
		}
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload)
		b.utmAskCost(ctx, uid)
		return true
	case "cost":
		costText := strings.TrimSpace(strings.Replace(text, ",", ".", 1))
		cost := 0.0
		if costText != "" {
			var err error
			cost, err = strconv.ParseFloat(costText, 64)
			if err != nil || cost < 0 || cost > 10000000 {
				_ = b.api.SendText(ctx, uid, "⚠️ Введите число (рубли, можно с копейками) или нажмите «⏭ Пропустить».")
				return true
			}
		}
		payload := map[string]any{"stage": "linktype", "cost": cost}
		for k, v := range sess.Payload {
			if k != "stage" && k != "cost" {
				payload[k] = v
			}
		}
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload)
		ns, _ := b.db.GetSessionPayload(ctx, uid)
		b.utmAskLinkType(ctx, uid, ns)
		return true
	case "post":
		postRef := strings.TrimSpace(text)
		if err := util.ValidatePostRef(postRef); err != nil {
			_ = b.api.SendText(ctx, uid, "⚠️ "+err.Error())
			return true
		}
		payload := map[string]any{"stage": "", "post_ref": postRef}
		for k, v := range sess.Payload {
			if k != "stage" && k != "post_ref" {
				payload[k] = v
			}
		}
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload)
		b.utmGenerate(ctx, uid)
		return true
	}
	return false
}

// utmGenerate — создать ссылку(и) из сессии и показать результат.
func (b *Bot) utmGenerate(ctx context.Context, uid int64) {
	sess, err := b.db.GetSessionPayload(ctx, uid)
	if err != nil || sess.State != store.SessionUTMCreate {
		_ = b.api.SendText(ctx, uid, "Сессия утеряна, начните заново.")
		return
	}
	mode, _ := sess.Payload["mode"].(string)
	source, _ := sess.Payload["source"].(string)
	postRef, _ := sess.Payload["post_ref"].(string)
	cost := 0.0
	if c, ok := sess.Payload["cost"].(float64); ok {
		cost = c
	}
	linkType, _ := sess.Payload["link_type"].(string)
	if linkType == "" {
		linkType = "unique"
	}
	_ = b.db.ClearSession(ctx, uid)

	botName := b.botUsername
	if botName == "" {
		botName = "ВАШ_БОТ"
	}

	if mode == "contest" {
		b.utmGenerateContest(ctx, uid, sess, source, botName)
		return
	}

	chID := utmSessChannel(sess)
	if chID == 0 {
		_ = b.api.SendText(ctx, uid, "Канал не выбран, начните заново.")
		return
	}
	code := util.RandomCode(10)
	if _, err := b.db.CreateUTMLink(ctx, uid, chID, source, "referral", source,
		"", "", postRef, code, cost, 0, linkType); err != nil {
		b.log.Warn("utm: create link", "err", err, "user", uid)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось создать ссылку. Попробуйте позже.")
		return
	}
	link := util.StartappLink(botName, "u_"+code)
	typeName := "Уникальная ссылка"
	leads := "ведёт на последний пост канала"
	if linkType == "super" {
		typeName = "Суперссылка"
		leads = "ведёт на пост: " + postRef
	}
	var costLine string
	if cost > 0 {
		costLine = fmt.Sprintf("\n💰 Стоимость: %.2f ₽ (посчитаем цену за подписчика)", cost)
	}
	title := ""
	if ch, err := b.db.GetChannel(ctx, chID, uid); err == nil {
		title = ifEmpty(ch.Title, fmt.Sprintf("канал #%d", ch.ChatID))
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("✅ %s для «%s» готова:\n\n⚡ %s\n\n%s%s\nНазвание: %s",
			typeName, title, link, leads, costLine, source),
		util.TrackingLinkKeyboard(link, chID))
}

// utmGenerateContest — по ссылке на каждый выбранный канал (одним запросом).
func (b *Bot) utmGenerateContest(ctx context.Context, uid int64, sess store.UserSession, source, botName string) {
	ids := utmSessChannelList(sess)
	if len(ids) == 0 {
		_ = b.api.SendText(ctx, uid, "Каналы не выбраны, начните заново.")
		return
	}
	var sb strings.Builder
	firstCh := int64(0)
	created := 0
	for _, chID := range ids {
		ch, err := b.db.GetChannel(ctx, chID, uid)
		if err != nil || !ch.IsBotAdmin {
			continue
		}
		code := util.RandomCode(10)
		if _, err := b.db.CreateUTMLink(ctx, uid, chID, source, "referral", source,
			"", "", "", code, 0, 0, "contest"); err != nil {
			b.log.Warn("utm: contest link", "err", err, "channel", chID)
			continue
		}
		title := ifEmpty(ch.Title, fmt.Sprintf("канал #%d", ch.ChatID))
		sb.WriteString(fmt.Sprintf("\n📢 %s\n⚡ %s\n", title, util.StartappLink(botName, "u_"+code)))
		if firstCh == 0 {
			firstCh = chID
		}
		created++
	}
	if created == 0 {
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось создать ссылки. Попробуйте позже.")
		return
	}
	word := "Конкурсная ссылка"
	if created > 1 {
		word = fmt.Sprintf("%d конкурсные ссылки", created)
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("🏁 %s готова (ведут на последний пост канала):\n%s\nНазвание: %s",
			word, sb.String(), source),
		util.TrackingLinkKeyboard("", firstCh))
}

// ---------- экраны статистики ----------

// showUTMMenu — «Ссылки и статистика канала»: сводка + вход в детали.
func (b *Bot) showUTMMenu(ctx context.Context, uid int64, p string) {
	idStr := strings.TrimPrefix(p, util.CBChannelUTM)
	chID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return
	}
	ch, err := b.db.GetChannel(ctx, chID, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Канал не найден.")
		return
	}
	title := ifEmpty(ch.Title, fmt.Sprintf("канал #%d", ch.ChatID))

	stats, _ := b.db.ListUTMStats(ctx, uid, chID)
	var totClicks, totSignups, totUnsubs int64
	for _, s := range stats {
		totClicks += s.Clicks
		totSignups += s.Signups
		totUnsubs += s.Unsubs
	}
	conv := 0.0
	if totClicks > 0 {
		conv = float64(totSignups) / float64(totClicks) * 100
	}

	kb := util.ChannelUTMKeyboard(chID)
	if url := util.ChannelURL(ch.Link); url != "" {
		kb.AddRow().AddClipboard("📋 Прямая ссылка (без счётчика)", url)
	}
	text := fmt.Sprintf("📊 %s\n\n", title) +
		fmt.Sprintf("Ссылок: %d\nКликов: %d\nПодписок: %d\nОтписок: %d\nКонверсия: %.1f%%\n\n",
			len(stats), totClicks, totSignups, totUnsubs, conv) +
		"Клик бесшовный: юзер → мини-апп (id фиксируется) → сразу канал/пост."
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, text, kb)
}

// showUTMStats — список ссылок кратко; детали — в миниаппе.
func (b *Bot) showUTMStats(ctx context.Context, uid int64, p string) {
	idStr := strings.TrimPrefix(p, util.CBUTMStats)
	chID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return
	}
	stats, err := b.db.ListUTMStats(ctx, uid, chID)
	if err != nil || len(stats) == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid, "📊 Ссылок пока нет. Создайте первую.", util.ChannelUTMKeyboard(chID))
		return
	}
	var sb strings.Builder
	sb.WriteString("📊 Ссылки канала (последние сверху; детали — в миниаппе):\n\n")
	for _, st := range stats {
		ret, err := b.db.GetUTMLinkRetention(ctx, st.ID)
		if err != nil {
			continue
		}
		icon := map[string]string{"unique": "🔗", "super": "⚡", "contest": "🏁"}[st.LinkType]
		sb.WriteString(fmt.Sprintf("%s %s\n  клики %d · подписки %d · осталось %d\n",
			icon, st.Source, st.Clicks, st.Signups, ret.StillIn))
	}
	kb := model.NewKeyboard()
	if b.botUsername != "" {
		kb.AddRow().AddLink("📱 Смотреть в миниаппе", util.StartappLink(b.botUsername, "analytics"))
	}
	kb.AddRow().AddCallBack("📎 Экспорт CSV", util.CBUTMExport+strconv.FormatInt(chID, 10))
	kb.AddRow().AddCallBack("← К каналу", util.CBChannelUTM+strconv.FormatInt(chID, 10))
	kb.AddRow().AddCallBack("🏠 В меню", util.CBMainMenu)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, sb.String(), kb)
}

// exportUTMCSV — выгрузка: русские заголовки, колонки по ревизии 13.11.
func (b *Bot) exportUTMCSV(ctx context.Context, uid int64, p string) {
	idStr := strings.TrimPrefix(p, util.CBUTMExport)
	chID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return
	}
	stats, err := b.db.ListUTMStats(ctx, uid, chID)
	if err != nil || len(stats) == 0 {
		_ = b.api.SendText(ctx, uid, "Нет данных для экспорта.")
		return
	}
	var csv strings.Builder
	csv.WriteString("Название;Код;Тип;Стоимость (₽);Клики;Всего подписались;Остались;Отписались;" +
		"Удержание 1д;Удержание 3д;Удержание 7д;Удержание 14д;Удержание 30д;" +
		"Цена за подписчика (грязная);Цена за подписчика (чистая);Конверсия (%)\n")
	for _, st := range stats {
		ret, _ := b.db.GetUTMLinkRetention(ctx, st.ID)
		conv := 0.0
		if st.Clicks > 0 {
			conv = float64(st.Signups) / float64(st.Clicks) * 100
		}
		cpaDirty, cpaClean := 0.0, 0.0
		if st.Cost > 0 && ret.Subscribed > 0 {
			cpaDirty = st.Cost / float64(ret.Subscribed)
		}
		if st.Cost > 0 && ret.StillIn > 0 {
			cpaClean = st.Cost / float64(ret.StillIn)
		}
		fmt.Fprintf(&csv, "%s;%s;%s;%.2f;%d;%d;%d;%d;%d;%d;%d;%d;%d;%.2f;%.2f;%.2f\n",
			csvEscape(st.Source), st.Code,
			map[string]string{"unique": "уникальная", "super": "супер", "contest": "конкурсная"}[st.LinkType],
			st.Cost, st.Clicks, ret.Subscribed, ret.StillIn, ret.Subscribed-ret.StillIn,
			ret.Retained1d, ret.Retained3d, ret.Retained7d, ret.Retained14d, ret.Retained30d,
			cpaDirty, cpaClean, conv)
	}

	filename := fmt.Sprintf("tracking_%d.csv", chID)
	if err := b.api.SendFile(ctx, uid, filename, []byte("\xEF\xBB\xBF"+csv.String())); err != nil {
		b.log.Warn("CSV file upload failed, sending as text", "err", err, "user", uid)
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"📎 CSV (файлом не удалось):\n\n"+csv.String(), util.ChannelUTMKeyboard(chID))
		return
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"✅ CSV-файл отправлен выше (открывается в Excel/Таблицах; разделитель — точка с запятой).",
		util.ChannelUTMKeyboard(chID))
}

// csvEscape — quoting + нейтрализация формул (leading = + - @ или таб).
func csvEscape(s string) string {
	if strings.ContainsAny(s, ";\"\n\r") {
		s = "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
	}
	if len(s) > 0 && strings.ContainsRune("=+-@\t", rune(s[0])) {
		return "'" + s
	}
	return s
}

func ifEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// showChannelDynamics — динамика канала (текст + график в миниаппе).
func (b *Bot) showChannelDynamics(ctx context.Context, uid int64, p string) {
	idStr := strings.TrimPrefix(p, util.CBUTMDyn)
	chID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return
	}
	ch, err := b.db.GetChannel(ctx, chID, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Канал не найден.")
		return
	}
	stats, err := b.db.ChannelDynamics(ctx, chID, 14)
	if err != nil {
		b.log.Warn("channel dynamics", "err", err, "channel", chID)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось загрузить динамику. Попробуйте позже.")
		return
	}

	var totJ, totU int
	maxN := 1
	for _, d := range stats {
		totJ += d.Joins
		totU += d.Unsubs
		if d.Joins > maxN {
			maxN = d.Joins
		}
	}
	var sb strings.Builder
	title := ifEmpty(ch.Title, fmt.Sprintf("канал #%d", ch.ChatID))
	sb.WriteString(fmt.Sprintf("📈 Динамика «%s» (14 дней, МСК):\n\n", title))
	for _, d := range stats {
		bar := strings.Repeat("█", (d.Joins*10)/maxN)
		if bar == "" && d.Joins > 0 {
			bar = "▏"
		}
		sb.WriteString(fmt.Sprintf("  %s %s +%d", d.Day, bar, d.Joins))
		if d.Unsubs > 0 {
			sb.WriteString(fmt.Sprintf(" (−%d)", d.Unsubs))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("\nИтого: +%d вступили, −%d отписались, итог %d", totJ, totU, totJ-totU))

	kb := model.NewKeyboard()
	if b.botUsername != "" {
		kb.AddRow().AddLink("📱 Графики в миниаппе", util.StartappLink(b.botUsername, "analytics"))
	}
	kb.AddRow().AddCallBack("← К каналу", util.CBChannelUTM+strconv.FormatInt(chID, 10))
	kb.AddRow().AddCallBack("🏠 В меню", util.CBMainMenu)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, sb.String(), kb)
}

// ---------- хелперы сессии ----------

func utmSessChannel(sess store.UserSession) int64 {
	if f, ok := sess.Payload["channel_id"].(float64); ok {
		return int64(f)
	}
	return 0
}

func utmSessChannels(sess store.UserSession) map[int64]bool {
	res := map[int64]bool{}
	if arr, ok := sess.Payload["channels"].([]any); ok {
		for _, v := range arr {
			if f, ok := v.(float64); ok {
				res[int64(f)] = true
			}
		}
	}
	return res
}

func utmSessChannelList(sess store.UserSession) []int64 {
	var res []int64
	for id := range utmSessChannels(sess) {
		res = append(res, id)
	}
	return res
}

// SendGiveawayPostToOwner — отправить готовый пост розыгрыша админу в чат
// (кнопка из миниаппа): текст + медиа + превью-кнопка участия точным видом.
func (b *Bot) SendGiveawayPostToOwner(ctx context.Context, uid int64, g store.Giveaway) error {
	buttonText := ifEmpty(g.ButtonText, "Участвовать")
	mediaSimple := make([]util.MediaTokenSimple, 0, len(g.MediaTokens))
	for _, mt := range g.MediaTokens {
		mediaSimple = append(mediaSimple, util.MediaTokenSimple{Type: mt.Type, Token: mt.Token})
	}
	attachments := util.GiveawayAttachments(buttonText, g.ID, mediaSimple, true)
	if err := b.api.SendWithAttachments(ctx, uid, g.PostText, attachments, model.TextFormat(g.PostFormat)); err != nil {
		return err
	}
	_ = b.api.SendText(ctx, uid, "⬆️ Пост розыгрыша «"+ifEmpty(g.Name, "без названия")+"» — как будет выглядеть в канале.")
	return nil
}

// GiveawayDraftFromApp — поля создания розыгрыша из миниаппа (все необязательные
// кроме поста/канала публикации; дефолты — как в чат-мастере).
type GiveawayDraftFromApp struct {
	Name           string   `json:"name"`
	PostText       string   `json:"post_text"`
	PostFormat     string   `json:"post_format"`
	MediaTokens    []store.MediaToken `json:"media_tokens"`
	ButtonText     string   `json:"button_text"`
	PubChannels    []int64  `json:"pub_channels"`
	SubChannels    []int64  `json:"sub_channels"`
	PublishNow     bool     `json:"publish_now"`
	PublishAt      string   `json:"publish_at"` // DD.MM.YYYY HH:MM (МСК) или ISO
	FinishMode     string   `json:"finish_mode"` // time | count
	FinishAt       string   `json:"finish_at"`
	FinishCount    int      `json:"finish_count"`
	Winners        int      `json:"winners"`
	WinnersMessage string   `json:"winners_message"`
	Referral       bool     `json:"referral"`
	Captcha        bool     `json:"captcha"`
	ResultsInPost  bool     `json:"results_in_post"`
	ResultsText    string   `json:"results_text"`
	SaveDraft      bool     `json:"save_draft"` // true → черновик, не планировать
}

// CreateGiveawayFromApp — создание розыгрыша из миниаппа: пишет поля черновика,
// валидирует (engine.Confirm) и планирует/публикует. Возвращает id и ошибку
// валидации (авторский текст).
func (b *Bot) CreateGiveawayFromApp(ctx context.Context, uid int64, in GiveawayDraftFromApp) (int64, string, error) {
	g, err := b.db.CreateDraftGiveaway(ctx, uid, "")
	if err != nil {
		return 0, "", err
	}
	set := func(field string, val any) {
		if err == nil {
			err = b.db.UpdateGiveawayFieldAny(ctx, g.ID, field, val)
		}
	}
	if in.Name != "" {
		set("name", in.Name)
	}
	if in.PostText != "" {
		set("post_text", in.PostText)
	}
	if in.PostFormat != "" {
		set("post_format", in.PostFormat)
	}
	if len(in.MediaTokens) > 0 {
		set("media_tokens", in.MediaTokens)
	}
	button := in.ButtonText
	if button == "" {
		button = "Участвовать"
	}
	set("button_text", button)
	if len(in.PubChannels) > 0 {
		set("publish_channel_ids", in.PubChannels)
	}
	if len(in.SubChannels) > 0 {
		set("subscribe_channel_ids", in.SubChannels)
	} else if len(in.PubChannels) > 0 {
		// Канал публикации всегда входит в подписки (как в чат-мастере).
		set("subscribe_channel_ids", in.PubChannels)
	}
	set("publish_now", in.PublishNow)
	if !in.PublishNow && in.PublishAt != "" {
		t, perr := util.ParseMoscowTime(in.PublishAt)
		if perr != nil {
			return g.ID, "Дата публикации: " + perr.Error(), nil
		}
		set("publish_at", t)
	}
	finishMode := in.FinishMode
	if finishMode == "" {
		finishMode = "time"
	}
	set("finish_mode", finishMode)
	switch finishMode {
	case "count":
		if in.FinishCount >= 10 {
			set("finish_participants_count", in.FinishCount)
		}
	default:
		if in.FinishAt != "" {
			t, perr := util.ParseMoscowTime(in.FinishAt)
			if perr != nil {
				return g.ID, "Дата итогов: " + perr.Error(), nil
			}
			set("finish_at", t)
		} else {
			// Дефолт: завтра 21:00 МСК.
			tom := time.Now().In(util.MoscowLocation()).AddDate(0, 0, 1)
			set("finish_at", time.Date(tom.Year(), tom.Month(), tom.Day(), 21, 0, 0, 0, util.MoscowLocation()).UTC())
		}
	}
	winners := in.Winners
	if winners < 1 {
		winners = 1
	}
	set("winners_count", winners)
	set("winners_message", in.WinnersMessage)
	set("referral_enabled", in.Referral)
	captcha := "none"
	if in.Captcha {
		captcha = "all"
	}
	set("captcha_mode", captcha)
	set("results_in_original_post", in.ResultsInPost)
	if in.ResultsText != "" {
		set("results_post_text", in.ResultsText)
	}
	if err != nil {
		return g.ID, "", err
	}
	if in.SaveDraft {
		return g.ID, "", nil
	}
	if err := b.engine.Confirm(ctx, g.ID); err != nil {
		var ve *fsm.ValidationError
		if e, ok := err.(*fsm.ValidationError); ok {
			ve = e
			return g.ID, ve.Msg, nil
		}
		return g.ID, "", err
	}
	// «Сразу» — немедленная публикация, без ожидания тика.
	if in.PublishNow && b.publishNow != nil {
		b.publishNow(ctx, g.ID)
	}
	return g.ID, "", nil
}

// CreateTrackingLinkFromApp — трекинг-ссылка из миниаппа: один вызов на любой
// тип (уникальная/супер/конкурс(пачка)) — та же логика, что в чат-флоу.
type TrackingLinkFromApp struct {
	Mode      string  `json:"mode"`       // ads | contest
	ChannelID int64   `json:"channel_id"` // ads
	ChannelIDs []int64 `json:"channel_ids"` // contest
	Name      string  `json:"name"`
	Cost      float64 `json:"cost"`
	LinkType  string  `json:"link_type"` // unique | super
	PostRef   string  `json:"post_ref"`
}

// CreateTrackingLinkFromApp возвращает коды созданных ссылок.
func (b *Bot) CreateTrackingLinkFromApp(ctx context.Context, uid int64, in TrackingLinkFromApp) ([]string, string, error) {
	if err := util.ValidateUTMSource(in.Name); err != nil {
		return nil, err.Error(), nil
	}
	botName := b.botUsername
	if botName == "" {
		botName = "ВАШ_БОТ"
	}
	var codes []string
	make := func(chID int64, linkType, postRef string, cost float64) string {
		code := util.RandomCode(10)
		if _, err := b.db.CreateUTMLink(ctx, uid, chID, in.Name, "referral", in.Name,
			"", "", postRef, code, cost, 0, linkType); err != nil {
			return ""
		}
		return code
	}
	if in.Mode == "contest" {
		for _, chID := range in.ChannelIDs {
			if _, err := b.db.GetChannel(ctx, chID, uid); err != nil {
				continue
			}
			if c := make(chID, "contest", "", 0); c != "" {
				codes = append(codes, c)
			}
		}
		if len(codes) == 0 {
			return nil, "Не выбраны каналы", nil
		}
		return codes, "", nil
	}
	if _, err := b.db.GetChannel(ctx, in.ChannelID, uid); err != nil {
		return nil, "Канал не найден", nil
	}
	linkType := in.LinkType
	if linkType == "" {
		linkType = "unique"
	}
	if c := make(in.ChannelID, linkType, in.PostRef, in.Cost); c != "" {
		return []string{c}, "", nil
	}
	return nil, "Не удалось создать ссылку", nil
}

// AddChannelFromApp — добавить канал из миниаппа по ссылке/@username.
// Возвращает название канала (или авторскую ошибку).
func (b *Bot) AddChannelFromApp(ctx context.Context, uid int64, link string) (string, string, error) {
	text := strings.TrimSpace(link)
	if !isChannelLink(text) {
		return "", "Похоже, это не ссылка на канал. Отправьте @username или https://max.ru/channel", nil
	}
	chat, err := b.api.GetChatByLink(ctx, text)
	if err != nil {
		return "", "Канал по ссылке не найден (он должен быть публичным)", nil
	}
	info, isAdmin, err := b.api.CheckChannelAdmin(ctx, chat.ChatID)
	if err != nil {
		return "", "Не удалось получить доступ к каналу. Добавьте бота админом и повторите", nil
	}
	if !isAdmin {
		return "", "Бот НЕ администратор в этом канале. Добавьте бота админом и повторите", nil
	}
	c, err := b.db.AddChannel(ctx, store.Channel{
		OwnerUserID: uid, ChatID: chat.ChatID, Title: info.Title, Link: info.Link,
		IsPublic: info.IsPublic, IsBotAdmin: true,
	})
	if err != nil {
		// Уже добавлен — не ошибка, вернём название.
		if ch, gerr := b.db.GetChannelByChat(ctx, uid, chat.ChatID); gerr == nil {
			return ifEmpty(ch.Title, "канал"), "", nil
		}
		return "", "Не удалось сохранить канал", nil
	}
	return ifEmpty(c.Title, "канал"), "", nil
}
