package handlers

import (
	"context"

	"fmt"
	"strconv"
	"strings"

	"github.com/maxbot/giveawaybot/internal/fsm"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// onMessageCallback — обработка нажатий на inline-кнопки.
func (b *Bot) onMessageCallback(ctx context.Context, u model.Update) {
	cb := u.GetCallback()
	uid := cb.User.UserID
	p := cb.Payload

	// 1. Кнопка "Участвовать" под постом розыгрыша — обрабатываем отдельно,
	//    т.к. onParticipate сам шлёт осмысленный alert через AnswerCallback.
	if strings.HasPrefix(p, util.CBParticipate) {
		b.onParticipate(ctx, uid, strings.TrimPrefix(p, util.CBParticipate), cb.CallbackID)
		return
	}

	// 2. Кнопки мастера создания — морфируют сообщение шага сами
	//    (AnswerCallbackEdit/уведомление), общий ответ не нужен.
	if isWizardPayload(p) {
		b.handleWizardCallback(ctx, uid, p, cb.CallbackID)
		return
	}

	// Прочие callback'и — снимаем "часики" с кнопки.
	defer b.api.AnswerCallback(ctx, cb.CallbackID, "")

	// 3. Главное меню и навигация
	b.handleMenuAction(ctx, uid, p)
}

// handleMenuAction — единый роутер действий главного меню/навигации.
// Вызывается из callback-кнопок (inline) и из текстовых подписей меню
// (fallback: сообщение от кнопки может прийти как текст).
func (b *Bot) handleMenuAction(ctx context.Context, uid int64, p string) {
	switch p {
	case util.CBMainMenu:
		_ = b.db.ClearSession(ctx, uid) // сброс любой активной сессии (UTM/captcha/etc)
		b.sendMainMenu(ctx, uid, "Главное меню:")
	case util.CBOldMenu:
		b.showOldMenu(ctx, uid)
	case util.CBCreateGiveaway:
		_ = b.db.ClearSession(ctx, uid) // гарантия: никаких висящих сессий при создании
		b.startCreate(ctx, uid)
	case util.CBMyGiveaways:
		b.showGiveawayFilters(ctx, uid)
	case util.CBMyChannels:
		b.showChannels(ctx, uid)
	case util.CBMyParticipations:
		b.showMyParticipations(ctx, uid)
	case util.CBAnalytics:
		b.showAnalyticsMenu(ctx, uid, "")
	case util.CBSupport:
		b.StartSupportDialog(ctx, uid)
	case util.CBChannelAdd:
		b.startAddChannel(ctx, uid)
	case util.CBChannelRefresh:
		b.refreshChannels(ctx, uid)
	case util.CBChannelBack:
		_ = b.db.ClearSession(ctx, uid)
		b.showChannels(ctx, uid)
	case util.CBChannelAnalytics:
		b.showChannelListForAnalytics(ctx, uid)
	case util.CBUTMQuickCreate:
		b.startQuickUTM(ctx, uid)
	default:
		// подпункты (фильтры/открыть/удалить и т.д.)
		b.handleSubPayload(ctx, uid, p)
	}
}

// isWizardPayload — кнопки мастера создания (морфят шаг сами, без общего
// ответа на нажатие): fsm_*, cal:*, тогглы каналов подписок.
func isWizardPayload(p string) bool {
	switch p {
	case util.CBFSMBack, util.CBFSMCancel, util.CBFSMNext, util.CBFSMExitYes,
		util.CBFSMConfirm, util.CBFSMReject, util.CBFSMHint, util.CBFSMContinue, util.CBFSMSave:
		return true
	}
	return strings.HasPrefix(p, util.CBFSMOption) ||
		strings.HasPrefix(p, util.CBFSMEdit) ||
		strings.HasPrefix(p, util.CBCal) ||
		strings.HasPrefix(p, util.CBChannelToggleSub)
}

// startCreate начинает создание розыгрыша.
func (b *Bot) startCreate(ctx context.Context, uid int64) {
	g, err := b.engine.StartDraft(ctx, uid)
	if err != nil {
		b.log.Warn("start draft", "err", err, "user", uid)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось начать создание. Попробуйте позже.")
		return
	}
	_ = b.db.ClearSession(ctx, uid)
	_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", nil)
	b.showStep(ctx, uid, g, g.DraftStep)
}

// handleSubPayload — подпункты меню (фильтры, открыть, удалить и т.д.).
func (b *Bot) handleSubPayload(ctx context.Context, uid int64, p string) {
	switch {
	case strings.HasPrefix(p, util.CBAnalyticsTab):
		tab := strings.TrimPrefix(p, util.CBAnalyticsTab)
		b.showAnalyticsMenu(ctx, uid, tab)

	case strings.HasPrefix(p, util.CBGiveawayFilter):
		status := strings.TrimPrefix(p, util.CBGiveawayFilter)
		b.showGiveawaysByStatus(ctx, uid, status)

	case strings.HasPrefix(p, util.CBGwOpen):
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, util.CBGwOpen), 10, 64)
		b.showGiveawayDetails(ctx, uid, id)

	case strings.HasPrefix(p, util.CBGwEditField):
		// gw_ef:<field>:<giveaway_id> — редактирование конкретного поля.
		rest := strings.TrimPrefix(p, util.CBGwEditField)
		parts := strings.Split(rest, ":")
		if len(parts) == 2 {
			fieldKey := parts[0]
			gid, _ := strconv.ParseInt(parts[1], 10, 64)
			if g, err := b.db.GetGiveaway(ctx, gid, uid); err == nil {
				b.startEditField(ctx, uid, g, fieldKey)
			}
		}

	case strings.HasPrefix(p, util.CBGwEditSet):
		// gw_es:<key>:<value>:<giveaway_id> — применение выбор-опции.
		rest := strings.TrimPrefix(p, util.CBGwEditSet)
		parts := strings.Split(rest, ":")
		if len(parts) == 3 {
			gid, _ := strconv.ParseInt(parts[2], 10, 64)
			b.applyEditSet(ctx, uid, parts[0], parts[1], gid)
		}

	case strings.HasPrefix(p, util.CBGwEditChTglSub), strings.HasPrefix(p, util.CBGwEditChTglPub):
		// gw_ets:/gw_etp:<channel_id>:<giveaway_id> — тоггл канала в редактуре.
		b.toggleEditChannel(ctx, uid, p)

	case strings.HasPrefix(p, util.CBGwMediaDone):
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, util.CBGwMediaDone), 10, 64)
		b.finishEditMedia(ctx, uid, id)

	case strings.HasPrefix(p, util.CBGwExtraWinner):
		// Доп. победитель в завершённом розыгрыше.
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, util.CBGwExtraWinner), 10, 64)
		b.pickExtraWinner(ctx, uid, id)

	case strings.HasPrefix(p, util.CBGwAnalytics):
		// Аналитика конкретного розыгрыша.
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, util.CBGwAnalytics), 10, 64)
		b.showGiveawayAnalytics(ctx, uid, id)

	case strings.HasPrefix(p, util.CBGwParticipants):
		// Список участников розыгрыша.
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, util.CBGwParticipants), 10, 64)
		b.showGiveawayParticipants(ctx, uid, id)

	case strings.HasPrefix(p, util.CBGwKickPart):
		// gw_kick:<giveaway_id>:<user_id> — удаление участника (анти-накрутка).
		rest := strings.TrimPrefix(p, util.CBGwKickPart)
		parts := strings.SplitN(rest, ":", 2)
		if len(parts) == 2 {
			gid, _ := strconv.ParseInt(parts[0], 10, 64)
			uid2, _ := strconv.ParseInt(parts[1], 10, 64)
			b.kickParticipant(ctx, uid, gid, uid2)
		}

	case strings.HasPrefix(p, util.CBGwCSVParts):
		// CSV-выгрузка участников.
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, util.CBGwCSVParts), 10, 64)
		b.exportParticipantsCSV(ctx, uid, id)

	case strings.HasPrefix(p, util.CBGwEdit):
		// «Продолжить создание» (для черновика) или «Редактировать» (scheduled/active — меню полей).
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, util.CBGwEdit), 10, 64)
		g, err := b.db.GetGiveaway(ctx, id, uid)
		if err != nil {
			_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
			return
		}
		if g.Status == store.StatusDraft {
			b.resumeGiveaway(ctx, uid, id) // черновик — продолжить FSM с сохранённого шага
		} else {
			b.showEditMenu(ctx, uid, id) // scheduled/active — меню редактирования полей
		}

	case strings.HasPrefix(p, util.CBGwCopy):
		// «Создать копию»: черновик с настройками завершённого розыгрыша
		// (контент/каналы/режимы), даты и publish_now не переносятся.
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, util.CBGwCopy), 10, 64)
		src, err := b.db.GetGiveaway(ctx, id, uid)
		if err != nil {
			_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
			return
		}
		g, err := b.db.CreateDraftFromGiveaway(ctx, uid, src)
		if err != nil {
			b.log.Warn("copy giveaway", "err", err, "id", id, "user", uid)
			_ = b.api.SendText(ctx, uid, "⚠️ Не удалось создать копию. Попробуйте позже.")
			return
		}
		b.showStep(ctx, uid, g, fsm.FirstStep)

	case strings.HasPrefix(p, util.CBGwCancel):
		// Отмена — как и остальные операции, только владельцем и только
		// scheduled/active: отменённый/завершённый розыгрыш трогать нельзя
		// (иначе crafted-callback мог «отменить» итоги и скрыть победителей).
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, util.CBGwCancel), 10, 64)
		g, err := b.db.GetGiveaway(ctx, id, uid)
		if err != nil {
			_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
			return
		}
		if g.Status != store.StatusScheduled && g.Status != store.StatusActive {
			_ = b.api.SendText(ctx, uid, "Отменить можно только запланированный или текущий розыгрыш.")
			return
		}
		if err := b.db.CancelGiveaway(ctx, id, uid); err != nil {
			b.log.Warn("cancel giveaway", "err", err, "id", id, "user", uid)
			_ = b.api.SendText(ctx, uid, "⚠️ Не удалось отменить розыгрыш. Попробуйте позже.")
			return
		}
		_ = b.api.SendText(ctx, uid, "Розыгрыш отменён.")

	case strings.HasPrefix(p, util.CBGwDelete):
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, util.CBGwDelete), 10, 64)
		if err := b.db.DeleteGiveaway(ctx, id, uid); err == nil {
			_ = b.api.SendText(ctx, uid, "🗑 Черновик удалён.")
			// Возврат к списку черновиков (не голое сообщение без кнопок).
			b.showGiveawaysByStatus(ctx, uid, store.StatusDraft)
		} else {
			_ = b.api.SendText(ctx, uid, "Нельзя удалить завершённый розыгрыш.")
		}

	case p == util.CBGwList:
		b.showGiveawayFilters(ctx, uid)

	case strings.HasPrefix(p, util.CBChannelUTM):
		// Выбор канала для генерации UTM-ссылки
		b.showUTMMenu(ctx, uid, p)

	case strings.HasPrefix(p, util.CBUTMMode):
		// Стартовый шаг: Конкурс / Реклама.
		mode := strings.TrimPrefix(p, util.CBUTMMode)
		if mode != "ads" && mode != "contest" {
			return
		}
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, map[string]any{
			"stage": "channel", "mode": mode,
		})
		sess, _ := b.db.GetSessionPayload(ctx, uid)
		if mode == "contest" {
			payload := map[string]any{"stage": "channels", "mode": mode}
			_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload)
			ns, _ := b.db.GetSessionPayload(ctx, uid)
			b.utmShowContestPicker(ctx, uid, ns)
			return
		}
		_ = sess
		b.utmShowChannelPicker(ctx, uid, sess)

	case p == util.CBUTMBack:
		b.utmBack(ctx, uid)

	case strings.HasPrefix(p, util.CBUTMQuickChSel):
		// Реклама: канал выбран (radio) → название.
		idStr := strings.TrimPrefix(p, util.CBUTMQuickChSel)
		chID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return
		}
		if _, err := b.db.GetChannel(ctx, chID, uid); err != nil {
			_ = b.api.SendText(ctx, uid, "Канал не найден.")
			return
		}
		sess, _ := b.db.GetSessionPayload(ctx, uid)
		payload := map[string]any{"stage": "name", "channel_id": float64(chID)}
		for k, v := range sess.Payload {
			if k != "stage" && k != "channel_id" && k != "channels" {
				payload[k] = v
			}
		}
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload)
		b.utmAskName(ctx, uid)

	case strings.HasPrefix(p, util.CBUTMToggle):
		// Конкурс: тоггл канала в мультивыборе.
		idStr := strings.TrimPrefix(p, util.CBUTMToggle)
		chID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return
		}
		sess, _ := b.db.GetSessionPayload(ctx, uid)
		sel := utmSessChannels(sess)
		if sel[chID] {
			delete(sel, chID)
		} else {
			sel[chID] = true
		}
		arr := make([]any, 0, len(sel))
		for id := range sel {
			arr = append(arr, float64(id))
		}
		payload := map[string]any{"stage": "channels", "channels": arr}
		for k, v := range sess.Payload {
			if k != "stage" && k != "channels" {
				payload[k] = v
			}
		}
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload)
		ns, _ := b.db.GetSessionPayload(ctx, uid)
		b.utmShowContestPicker(ctx, uid, ns)

	case p == util.CBUTMReady:
		// Конкурс: каналы выбраны → название (фиксируем шаг в сессии).
		sess, err := b.db.GetSessionPayload(ctx, uid)
		if err != nil || sess.State != store.SessionUTMCreate {
			return
		}
		payload := map[string]any{"stage": "name"}
		for k, v := range sess.Payload {
			if k != "stage" {
				payload[k] = v
			}
		}
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload)
		b.utmAskName(ctx, uid)

	case p == util.CBUTMSkipCost:
		// Стоимость пропущена → тип ссылки.
		sess, _ := b.db.GetSessionPayload(ctx, uid)
		payload := map[string]any{"stage": "linktype", "cost": 0.0}
		for k, v := range sess.Payload {
			if k != "stage" && k != "cost" {
				payload[k] = v
			}
		}
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload)
		ns, _ := b.db.GetSessionPayload(ctx, uid)
		b.utmAskLinkType(ctx, uid, ns)

	case strings.HasPrefix(p, util.CBUTMType):
		// Тип ссылки: уникальная — сразу генерация; супер — ввод поста.
		lt := strings.TrimPrefix(p, util.CBUTMType)
		if lt != "unique" && lt != "super" {
			return
		}
		sess, _ := b.db.GetSessionPayload(ctx, uid)
		if lt == "unique" {
			payload := map[string]any{"stage": "", "link_type": "unique"}
			for k, v := range sess.Payload {
				if k != "stage" && k != "link_type" {
					payload[k] = v
				}
			}
			_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload)
			b.utmGenerate(ctx, uid)
			return
		}
		payload := map[string]any{"stage": "post", "link_type": "super"}
		for k, v := range sess.Payload {
			if k != "stage" && k != "link_type" {
				payload[k] = v
			}
		}
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload)
		ns, _ := b.db.GetSessionPayload(ctx, uid)
		b.utmAskPost(ctx, uid, ns)

	case p == util.CBUTMGenContest:
		b.utmGenerate(ctx, uid)

	case strings.HasPrefix(p, util.CBUTMCreate):
		// Из меню канала: новая ссылка на этот канал (флоу Рекламы).
		idStr := strings.TrimPrefix(p, util.CBUTMCreate)
		chID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return
		}
		if _, err := b.db.GetChannel(ctx, chID, uid); err != nil {
			_ = b.api.SendText(ctx, uid, "Канал не найден.")
			return
		}
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, map[string]any{
			"stage": "name", "mode": "ads", "channel_id": float64(chID),
		})
		b.utmAskName(ctx, uid)

	case strings.HasPrefix(p, util.CBUTMDyn):
		b.showChannelDynamics(ctx, uid, p)

	case strings.HasPrefix(p, util.CBUTMStats):
		// Показать аналитику UTM по каналу
		b.showUTMStats(ctx, uid, p)

	case strings.HasPrefix(p, util.CBUTMExport):
		// Экспорт CSV аналитики
		b.exportUTMCSV(ctx, uid, p)

	default:
		b.sendMainMenu(ctx, uid, "Главное меню:")
	}
}

// resumeGiveaway — продолжает создание черновика (или открывает активный для правок).
// Для черновика: активирует FSM на текущем шаге и показывает его.
func (b *Bot) resumeGiveaway(ctx context.Context, uid, id int64) {
	g, err := b.engine.ResumeDraft(ctx, id)
	if err != nil {
		b.log.Warn("resume draft", "err", err, "id", id, "user", uid)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось продолжить создание. Попробуйте позже.")
		return
	}
	if g.OwnerUserID != uid {
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return
	}
	// Делаем этот черновик «самым свежим»: все FSM-кнопки работают с последним
	// draft юзера (GetDraftGiveaway ORDER BY updated_at) — иначе при нескольких
	// черновиках нажатия уводили бы в другой черновик.
	_ = b.db.AbandonDraft(ctx, id) // только трогает updated_at, шаг сохраняется
	b.showStep(ctx, uid, g, g.DraftStep)
}

// fmtErr — короткий текст ошибки для пользователя.
func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf(" (%v)", err)
}
