package handlers

import (
	"context"
	"errors"
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

	// Прочие callback'и — снимаем "часики" с кнопки.
	defer b.api.AnswerCallback(ctx, cb.CallbackID, "")

	// 2. Кнопки FSM создания розыгрыша
	if isFSMPayload(p) {
		b.handleFSMCallback(ctx, uid, p, cb.CallbackID)
		return
	}

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
		kb := model.NewKeyboard()
		if b.helpURL != "" {
			kb.AddRow().AddLink("📚 Полная справка", b.helpURL)
		}
		kb.AddRow().AddCallBack("🏠 В меню", util.CBMainMenu)
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"🆘 Поддержка\n\n📚 Полное руководство — кнопка ниже (главы: создание, каналы, редактура, аналитика, FAQ).\n\n"+
				"По другим вопросам: @your_support_contact", kb)
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

// isFSMPayload определяет кнопки FSM (навигация/опции/подтверждение/подсказка).
func isFSMPayload(p string) bool {
	switch p {
	case util.CBFSMBack, util.CBFSMCancel, util.CBFSMSkip, util.CBFSMConfirm, util.CBFSMReject, util.CBFSMHint, util.CBFSMContinue:
		return true
	}
	return strings.HasPrefix(p, util.CBFSMOption)
}

// startCreate начинает создание розыгрыша.
func (b *Bot) startCreate(ctx context.Context, uid int64) {
	g, err := b.engine.StartDraft(ctx, uid)
	if err != nil {
		b.log.Warn("start draft", "err", err, "user", uid)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось начать создание. Попробуйте позже.")
		return
	}
	b.showStep(ctx, uid, g, g.DraftStep)
}

// handleFSMCallback обрабатывает кнопки FSM (навигация/опции/подтверждение/подсказка).
func (b *Bot) handleFSMCallback(ctx context.Context, uid int64, p string, callbackID string) {
	g, err := b.db.GetDraftGiveaway(ctx, uid)
	if err != nil {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"Создание не активно. Начните заново.", util.MainMenuKeyboard())
		return
	}

	switch {
	case p == util.CBFSMCancel:
		_ = b.engine.Abandon(ctx, g.ID)
		// Черновик без названия — мусор, удаляем. С названием — сохраняем.
		if g.Name != "" {
			b.sendMainMenu(ctx, uid, "✖ Создание отменено. Черновик сохранён в «Мои розыгрыши → Черновики».")
		} else {
			_ = b.db.DeleteGiveaway(ctx, g.ID, uid)
			b.sendMainMenu(ctx, uid, "✖ Создание отменено.")
		}

	case p == util.CBFSMBack:
		prev, _ := b.engine.GoBack(ctx, g.ID, g.DraftStep, g)
		// перечитываем, чтобы актуальные данные
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.showStep(ctx, uid, gg, prev)

	case p == util.CBFSMSkip:
		if err := b.engine.SkipStep(ctx, g); err != nil {
			b.api.AnswerCallback(ctx, "", err.Error())
			return
		}
		b.advance(ctx, uid, g.ID, g.DraftStep)

	case p == util.CBFSMConfirm:
		if err := b.engine.Confirm(ctx, g.ID); err != nil {
			var ve *fsm.ValidationError
			msg := "⚠️ Не удалось подтвердить розыгрыш. Попробуйте позже."
			if errors.As(err, &ve) {
				msg = "⚠️ " + ve.Msg //ValidationError — авторский текст, internals не утекают
			} else {
				b.log.Warn("confirm", "err", err, "id", g.ID, "user", uid)
			}
			// Ошибка валидации на предпросмотре — предлагаем вернуться на
			// соотв. шаг для исправления (не голый текст без кнопок).
			_, _ = b.api.SendTextWithKeyboard(ctx, uid,
				msg+"\n\nНажмите «✏ Исправить» и пройдите к нужному шагу, или «✖ Отменить».",
				util.ConfirmErrorKeyboard(g.ID))
			return
		}
		// «Опубликовать сразу»: Confirm уже выставил publish_at=now —
		// перечитываем и публикуем немедленно, без ожидания тика.
		published := false
		if b.publishNow != nil {
			if gg, err := b.db.GetGiveawayAny(ctx, g.ID); err == nil && gg.PublishNow {
				b.publishNow(ctx, gg.ID)
				published = true
			}
		}
		if published {
			b.sendMainMenu(ctx, uid, "✅ Розыгрыш опубликован! Пост уже в каналах, кнопка «Участвовать» активна.")
		} else {
			b.sendMainMenu(ctx, uid, "✅ Розыгрыш запланирован! Пост будет опубликован автоматически в назначенное время. Следите в «Мои розыгрыши → Запланированные».")
		}

	case p == util.CBFSMReject:
		_ = b.engine.Abandon(ctx, g.ID)
		// Черновик без названия — мусор, удаляем. С названием — сохраняем.
		if g.Name != "" {
			b.sendMainMenu(ctx, uid, "❌ Создание отменено. Черновик сохранён в «Мои розыгрыши → Черновики».")
		} else {
			_ = b.db.DeleteGiveaway(ctx, g.ID, uid)
			b.sendMainMenu(ctx, uid, "❌ Создание отменено.")
		}

	case p == util.CBFSMHint:
		// Подсказка по текущему шагу — СООБЩЕНИЕМ (алертов в MAX нет):
		// под ним «Продолжить» (возврат к шагу) и «Отменить».
		if hint := fsm.HintForStep(g.DraftStep); hint != "" {
			_, _ = b.api.SendTextWithKeyboard(ctx, uid, "💡 "+hint, util.HintKeyboard())
		}

	case p == util.CBFSMContinue:
		// Возврат к шагу, на котором нажали лампочку (draft_step не менялся).
		b.showStep(ctx, uid, g, g.DraftStep)

	case strings.HasPrefix(p, util.CBFSMOption):
		key := strings.TrimPrefix(p, util.CBFSMOption)
		b.handleOption(ctx, uid, g, key)
	}
}

// handleOption обрабатывает опционные кнопки шагов (выбор каналов, режимы и т.д.).
// Рефералы и продвижение убраны из MVP.
func (b *Bot) handleOption(ctx context.Context, uid int64, g store.Giveaway, key string) {
	switch key {
	case "next":
		// Шаги sub/pub channels: валидируем выбранные каналы ПЕРЕД переходом
		// (ошибка на месте, а не в конце создания).
		if g.DraftStep == fsm.StepPubChannels {
			if len(g.PublishChannelIDs) == 0 {
				_ = b.api.SendText(ctx, uid,
					"⚠️ Выберите хотя бы один канал публикации (отметьте ✅) — без него пост некуда публиковать.")
				return
			}
			// Проверяем админство бота в выбранных каналах — ЖИВЫМ запросом,
			// не кэшем: флаг is_bot_admin в БД может протухнуть (бота кикнули,
			// сменился токен = новый бот без админства). Ретраи публикации в
			// мёртвый канал — именно эта дыра (chat.not.found в scheduler).
			chs, err := b.db.ChannelsByIDs(ctx, uid, g.PublishChannelIDs)
			if err == nil {
				var bad []string
				for _, c := range chs {
					live, err := b.api.IsBotAdminInChat(ctx, c.ChatID)
					if live != c.IsBotAdmin {
						_ = b.db.SetChannelAdminFlag(ctx, c.ID, live) // кэш → правда
					}
					if err != nil || !live {
						title := c.Title
						if title == "" {
							title = fmt.Sprintf("канал #%d", c.ChatID)
						}
						bad = append(bad, title)
					}
				}
				if len(bad) > 0 {
					_ = b.api.SendText(ctx, uid,
						"⚠️ Бот не админ в каналах: "+strings.Join(bad, ", ")+
							".\nДобавьте бота админом в канал и передобавьте его в «Мои каналы ➕» (перешлите пост из канала).")
					return
				}
			}
		}
		b.advance(ctx, uid, g.ID, g.DraftStep)

	case "publish_now":
		// «Опубликовать сразу»: НЕ ставим publish_at тут (пока юзер идёт по
		// оставшимся шагам, время бы «прошло»). Только флаг — publish_at
		// будет перезаписан на now в момент «Принять» (см. engine.Confirm).
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "publish_now", true)
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.advance(ctx, uid, g.ID, gg.DraftStep)

	case "publish_later":
		// «По времени» — следующий шаг publish_at (ввод даты).
		b.advance(ctx, uid, g.ID, g.DraftStep)

	case "finish_time":
		// «По времени» — движок пропустит finish_count по finish_mode.
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "finish_mode", "time")
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.advance(ctx, uid, g.ID, gg.DraftStep)

	case "finish_count":
		// «По количеству участников» — движок пропустит finish_at.
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "finish_mode", "count")
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.advance(ctx, uid, g.ID, gg.DraftStep)

	case "results_original":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "results_in_original_post", true)
		// «В исходном посте» — текст итогов не нужен, движок пропустит шаг.
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.advance(ctx, uid, g.ID, gg.DraftStep)
	case "results_separate":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "results_in_original_post", false)
		// «Отдельным постом» — запросить текст итогов (след. шаг results_text).
		b.advance(ctx, uid, g.ID, g.DraftStep)

	case "referral_on":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "referral_enabled", true)
		b.advance(ctx, uid, g.ID, g.DraftStep)
	case "referral_off":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "referral_enabled", false)
		b.advance(ctx, uid, g.ID, g.DraftStep)
	case "captcha_none":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "captcha_mode", store.CaptchaNone)
		b.advance(ctx, uid, g.ID, g.DraftStep)
	case "captcha_all":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "captcha_mode", store.CaptchaAll)
		b.advance(ctx, uid, g.ID, g.DraftStep)
	}
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

	case strings.HasPrefix(p, util.CBChannelToggleSub), strings.HasPrefix(p, util.CBChannelTogglePub):
		b.toggleChannelSelection(ctx, uid, p)

	case strings.HasPrefix(p, util.CBUTMSkipPost):
		// Билдер трекинг-ссылки: пропустить пост-источник → сразу стоимость.
		sess, err := b.db.GetSessionPayload(ctx, uid)
		if err != nil || sess.State != store.SessionUTMCreate {
			return
		}
		source, _ := sess.Payload["source"].(string)
		chIDf, _ := sess.Payload["channel_id"].(float64)
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, map[string]any{
			"channel_id": chIDf, "stage": "cost", "source": source, "post_ref": "",
		})
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"💰 Стоимость выхода (необязательно).\n\n"+
				"Сколько заплачено за размещение (₽)? Посчитаем CPA — цену подписчика. Можно пропустить.",
			util.CostKeyboard())

	case strings.HasPrefix(p, util.CBUTMSkipCost):
		sess, err := b.db.GetSessionPayload(ctx, uid)
		if err != nil || sess.State != store.SessionUTMCreate {
			return
		}
		source, _ := sess.Payload["source"].(string)
		postRef, _ := sess.Payload["post_ref"].(string)
		chIDf, _ := sess.Payload["channel_id"].(float64)
		b.finishUTMLink(ctx, uid, int64(chIDf), source, postRef, 0)

	case strings.HasPrefix(p, util.CBChannelUTM):
		// Выбор канала для генерации UTM-ссылки
		b.showUTMMenu(ctx, uid, p)

	case strings.HasPrefix(p, util.CBUTMQuickLink):
		// Простая ссылка одним тапом: без вопросов, source = «Простая <дата>».
		// Пришло/ушло считается; UTM-детализация — через билдер.
		idStr := strings.TrimPrefix(p, util.CBUTMQuickLink)
		chID, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			return
		}
		b.createSimpleUTMLink(ctx, uid, chID)

	case strings.HasPrefix(p, util.CBUTMCreate):
		// Начать создание UTM-ссылки на канал
		b.startCreateUTM(ctx, uid, p)

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
