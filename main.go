package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	yandexgpt "github.com/sheeiavellie/go-yandexgpt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Scores хранит веса для каждого варианта развёртывания.
type Scores struct {
	OnPrem  int
	Private int
	Public  int
}

// Criterion описывает один критерий.
type Criterion struct {
	Name        string
	Category    string
	BaseScores  Scores
	Description string
	IsSpecial   bool // если true, нужно уточнять у пользователя конкретный вариант
}

// Добавим в структуру UserState поля для процесса переопределения
type UserState struct {
	Step               int
	SelectedCriteria   []string          // список названий критериев, которые пользователь выбрал
	CriteriaPriorities map[string]int    // приоритет для каждого критерия
	OverriddenScores   map[string]Scores // переопределённые баллы
	SpecialValues      map[string]string // выбранные «уровни» для спецкритериев
	CurrentCriterion   string            // текущий критерий, для которого устанавливается приоритет
	CriteriaMessageID  int               // ID сообщения с кнопками критериев
	PriorityMessageID  int               // ID сообщения с кнопками приоритетов
	SpecialMessageID   int               // ID сообщения с кнопками для спец. критериев
	OverrideStep       int               // Какой вес изменяется (0 - OnPrem, 1 - Private, 2 - Public)
	TempOverride       Scores            // Временное хранение переопределяемых весов
	OverrideMessageID  int               // ID сообщения с кнопками переопределения
	CurrentOverride    string
}

// Для простоты — глобальные переменные.
var (
	botToken        = os.Getenv("BOT_TOKEN")
	userStates      = make(map[int64]*UserState) // key = chatID
	defaultCriteria = getDefaultCriteria()
	logger          *CustomLogger
)

// Инициализация критериев
func getDefaultCriteria() []Criterion {
	return []Criterion{
		{
			Name:        "Юрисдикция данных",
			Category:    "Регуляторные и безопасность",
			BaseScores:  Scores{OnPrem: 8, Private: 5, Public: 4},
			Description: "Насколько важна локализация данных и соответствие местным законам.",
		},
		{
			Name:        "Отраслевые стандарты",
			Category:    "Регуляторные и безопасность",
			BaseScores:  Scores{OnPrem: 9, Private: 8, Public: 5},
			Description: "Требования к сертификации и соответствию отраслевым нормам.",
		},
		{
			Name:        "Физическая безопасность",
			Category:    "Регуляторные и безопасность",
			BaseScores:  Scores{OnPrem: 5, Private: 4, Public: 3},
			Description: "Насколько важно физическое расположение серверов и меры их защиты.",
		},
		{
			Name:        "Объём данных",
			Category:    "Технические",
			BaseScores:  Scores{OnPrem: 0, Private: 0, Public: 0},
			Description: "Объём хранимых данных (зависит от масштаба).",
			IsSpecial:   true,
		},
		{
			Name:        "Латентность",
			Category:    "Технические",
			BaseScores:  Scores{OnPrem: 8, Private: 6, Public: 5},
			Description: "Требования к задержкам при доступе к данным.",
		},
		{
			Name:        "Вариативность нагрузки",
			Category:    "Технические",
			BaseScores:  Scores{OnPrem: 9, Private: 8, Public: 8},
			Description: "Насколько часто и сильно меняется нагрузка на БД.",
		},
		{
			Name:        "Начальные инвестиции",
			Category:    "Экономические",
			BaseScores:  Scores{OnPrem: 3, Private: 4, Public: 8},
			Description: "Начальные затраты на развёртывание.",
		},
		{
			Name:        "Постоянные затраты",
			Category:    "Экономические",
			BaseScores:  Scores{OnPrem: 7, Private: 8, Public: 9},
			Description: "Регулярные расходы на поддержку, лицензии и т.д.",
		},
		{
			Name:        "Срок использования",
			Category:    "Экономические",
			BaseScores:  Scores{OnPrem: 0, Private: 0, Public: 0},
			Description: "Как долго планируется использовать систему (зависит от срока).",
			IsSpecial:   true,
		},
		{
			Name:        "Квалификация персонала",
			Category:    "Организационные",
			BaseScores:  Scores{OnPrem: 7, Private: 8, Public: 9},
			Description: "Есть ли в команде экспертиза по управлению и настройке БД.",
		},
		{
			Name:        "Время до запуска",
			Category:    "Организационные",
			BaseScores:  Scores{OnPrem: 8, Private: 9, Public: 9},
			Description: "Насколько быстро нужно развернуть систему.",
		},
		{
			Name:        "Масштабируемость",
			Category:    "Организационные",
			BaseScores:  Scores{OnPrem: 7, Private: 9, Public: 9},
			Description: "Требования к быстрому масштабированию под нагрузку.",
		},
	}
}

func main() {
	logger = NewLogger(true)

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		logger.Printf("Ошибка инициализации бота: %v", err)
		log.Panic(err)
	}

	// Отключаем встроенное логирование библиотеки
	bot.Debug = false

	logger.Printf("Авторизован как %s", bot.Self.UserName)

	u := tgbotapi.NewUpdate(0)
	u.Timeout = 60
	updates := bot.GetUpdatesChan(u)

	for update := range updates {
		if update.Message != nil {
			chatID := update.Message.Chat.ID
			text := update.Message.Text

			logger.LogTelegramAction("Получено сообщение", map[string]interface{}{
				"ID чата": chatID,
				"Текст":   text,
				"От":      update.Message.From.UserName,
			})

			// Инициализируем состояние пользователя, если нужно
			if userStates[chatID] == nil {
				userStates[chatID] = &UserState{
					Step:               0,
					SelectedCriteria:   []string{},
					CriteriaPriorities: make(map[string]int),
					OverriddenScores:   make(map[string]Scores),
					SpecialValues:      make(map[string]string),
				}
			}

			if text == "/start" {
				msg := tgbotapi.NewMessage(chatID, "Привет! Я бот для выбора типа СУБД (On-Premise, Private, Public). Давайте начнём чеклист.")
				userStates[chatID] = &UserState{
					Step:               1,
					SelectedCriteria:   []string{},
					CriteriaPriorities: make(map[string]int),
					OverriddenScores:   make(map[string]Scores),
					SpecialValues:      make(map[string]string),
				}
				sendMessage(bot, msg)
				showCriteriaButtons(bot, chatID)
			} else if text == "/reset" {
				userStates[chatID] = &UserState{
					Step:               1,
					SelectedCriteria:   []string{},
					CriteriaPriorities: make(map[string]int),
					OverriddenScores:   make(map[string]Scores),
					SpecialValues:      make(map[string]string),
				}
				msg := tgbotapi.NewMessage(chatID, "Чеклист сброшен. Давайте начнем заново.")
				sendMessage(bot, msg)
				showCriteriaButtons(bot, chatID)
			} else if state := userStates[chatID]; state.Step == 5 {
				// Обработка ввода переопределенных баллов
				processScoreOverride(bot, chatID, text)
			}
		} else if update.CallbackQuery != nil {
			// Обрабатываем нажатия на кнопки
			chatID := update.CallbackQuery.Message.Chat.ID

			logCallbackQuery(update.CallbackQuery)

			callback := tgbotapi.NewCallback(update.CallbackQuery.ID, "")
			_, err := bot.Request(callback)
			if err != nil {
				logger.Printf("Ошибка при ответе на callback: %v", err)
			}

			handleCallbackQuery(bot, update.CallbackQuery, chatID)
		}
	}
}

// Показывает список критериев в виде кнопок
func showCriteriaButtons(bot *tgbotapi.BotAPI, chatID int64) {
	state := userStates[chatID]
	var keyboardRows [][]tgbotapi.InlineKeyboardButton

	// Добавляем все критерии без группировки по категориям
	for _, crit := range defaultCriteria {
		isSelected := contains(state.SelectedCriteria, crit.Name)

		buttonText := crit.Name
		if isSelected {
			buttonText = "✓ " + buttonText
		} else {
			buttonText = "○ " + buttonText
		}

		keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData(buttonText, "crit_"+crit.Name),
		})
	}

	// Добавляем кнопку "Готово"
	keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("✅ Готово", "done_criteria"),
	})

	// Создаем клавиатуру
	keyboard := tgbotapi.NewInlineKeyboardMarkup(keyboardRows...)

	// Если у нас уже есть ID сообщения с кнопками, обновляем его
	if state.CriteriaMessageID != 0 {
		editMsg := tgbotapi.NewEditMessageReplyMarkup(
			chatID,
			state.CriteriaMessageID,
			keyboard,
		)
		if _, err := editMessageReplyMarkup(bot, editMsg); err != nil {
			logger.Printf("Ошибка обновления сообщения: %v", err)
		}
	} else {
		// Иначе отправляем новое сообщение
		msg := tgbotapi.NewMessage(chatID, "Выберите критерии, которые важны для вашей компании:")
		msg.ReplyMarkup = keyboard

		sentMsg, err := sendMessage(bot, msg)
		if err == nil {
			state.CriteriaMessageID = sentMsg.MessageID
		} else {
			logger.Printf("Ошибка отправки сообщения: %v", err)
		}
	}
}

// Эту функцию нужно добавить для преобразования callback в значения
func getSpecialValueFromCallback(callbackPrefix string, index int) string {
	switch callbackPrefix {
	case "sdata": // Объём данных
		options := []string{"Малый", "Средний", "Большой"}
		if index >= 0 && index < len(options) {
			return options[index]
		}
	case "susage": // Срок использования
		options := []string{"Краткосрочный", "Долгосрочный"}
		if index >= 0 && index < len(options) {
			return options[index]
		}
	case "sother":
		options := []string{"Опция 1", "Опция 2", "Опция 3"}
		if index >= 0 && index < len(options) {
			return options[index]
		}
	}
	return ""
}

// Функция, возвращающая критерий по префиксу
func getSpecialCriterionName(callbackPrefix string) string {
	switch callbackPrefix {
	case "sdata":
		return "Объём данных"
	case "susage":
		return "Срок использования"
	case "sother":
		return "Другой критерий" // замените на нужное, если добавите другие критерии
	}
	return ""
}

// Обрабатывает нажатия на кнопки
func handleCallbackQuery(bot *tgbotapi.BotAPI, query *tgbotapi.CallbackQuery, chatID int64) {
	state := userStates[chatID]
	callbackData := query.Data

	// Обработка специальных критериев с новым форматом
	if strings.HasPrefix(callbackData, "sdata_") ||
		strings.HasPrefix(callbackData, "susage_") ||
		strings.HasPrefix(callbackData, "sother_") {

		parts := strings.Split(callbackData, "_")
		if len(parts) == 2 {
			prefix := parts[0]
			index, err := strconv.Atoi(parts[1])

			if err == nil {
				criterionName := getSpecialCriterionName(prefix)
				value := getSpecialValueFromCallback(prefix, index)

				if criterionName != "" && value != "" {
					state.SpecialValues[criterionName] = value

					logger.LogTelegramAction("Выбрано специальное значение", map[string]interface{}{
						"Критерий": criterionName,
						"Значение": value,
					})

					// Проверяем, все ли спец. значения установлены
					allSpecialSet := true
					for _, critName := range state.SelectedCriteria {
						crit := findCriterionByName(critName)
						if crit.IsSpecial && state.SpecialValues[critName] == "" {
							allSpecialSet = false
							break
						}
					}

					if allSpecialSet {
						state.Step = 4
						askOverride(bot, chatID)
					} else {
						showSpecialCriteriaOptions(bot, chatID)
					}
				}
			}
		}
		return
	}

	if strings.HasPrefix(callbackData, "crit_") {
		// Выбор/отмена выбора критерия
		criterionName := strings.TrimPrefix(callbackData, "crit_")

		// Переключаем состояние выбора
		if contains(state.SelectedCriteria, criterionName) {
			// Удаляем критерий из выбранных
			for i, name := range state.SelectedCriteria {
				if name == criterionName {
					state.SelectedCriteria = append(state.SelectedCriteria[:i], state.SelectedCriteria[i+1:]...)
					break
				}
			}
			logger.LogTelegramAction("Критерий отменен", map[string]interface{}{
				"Критерий": criterionName,
			})
		} else {
			// Добавляем критерий к выбранным
			state.SelectedCriteria = append(state.SelectedCriteria, criterionName)
			logger.LogTelegramAction("Критерий выбран", map[string]interface{}{
				"Критерий": criterionName,
			})
		}

		// Обновляем сообщение с кнопками критериев
		showCriteriaButtons(bot, chatID)
	} else if callbackData == "done_criteria" {
		// Пользователь закончил выбор критериев
		if len(state.SelectedCriteria) == 0 {
			msg := tgbotapi.NewMessage(chatID, "Пожалуйста, выберите хотя бы один критерий.")
			sendMessage(bot, msg)
			showCriteriaButtons(bot, chatID)
		} else {
			logger.LogTelegramAction("Завершен выбор критериев", map[string]interface{}{
				"Выбрано критериев": len(state.SelectedCriteria),
				"Критерии":          state.SelectedCriteria,
			})
			state.Step = 2
			startPrioritySelection(bot, chatID)
		}
	} else if strings.HasPrefix(callbackData, "prio_") {
		// Выбор приоритета для критерия
		parts := strings.Split(callbackData, "_")
		if len(parts) == 3 {
			criterionName := parts[1]
			priority, _ := strconv.Atoi(parts[2])

			state.CriteriaPriorities[criterionName] = priority
			logger.LogTelegramAction("Установлен приоритет", map[string]interface{}{
				"Критерий":  criterionName,
				"Приоритет": priority,
			})

			// Проверяем, все ли приоритеты установлены
			if len(state.CriteriaPriorities) == len(state.SelectedCriteria) {
				// Проверяем, есть ли спец. критерии
				hasSpecialCriteria := false
				for _, critName := range state.SelectedCriteria {
					crit := findCriterionByName(critName)
					if crit.IsSpecial {
						hasSpecialCriteria = true
						break
					}
				}

				if hasSpecialCriteria {
					state.Step = 3
					showSpecialCriteriaOptions(bot, chatID)
				} else {
					state.Step = 4
					askOverride(bot, chatID)
				}
			} else {
				// Если не все приоритеты установлены, показываем следующий
				startPrioritySelection(bot, chatID)
			}
		}
	} else if strings.HasPrefix(callbackData, "special_") {
		// Выбор значения для специального критерия
		parts := strings.Split(callbackData, "_")
		if len(parts) == 3 {
			criterionName := parts[1]
			value := parts[2]

			state.SpecialValues[criterionName] = value
			logger.LogTelegramAction("Выбрано специальное значение", map[string]interface{}{
				"Критерий": criterionName,
				"Значение": value,
			})

			// Проверяем, все ли спец. значения установлены
			allSpecialSet := true
			for _, critName := range state.SelectedCriteria {
				crit := findCriterionByName(critName)
				if crit.IsSpecial && state.SpecialValues[critName] == "" {
					allSpecialSet = false
					break
				}
			}

			if allSpecialSet {
				state.Step = 4
				askOverride(bot, chatID)
			} else {
				showSpecialCriteriaOptions(bot, chatID)
			}
		}
	}

	if callbackData == "override_yes" {
		// Вместо прямого перехода в состояние ввода текста, показываем список критериев
		showOverrideCriteriaList(bot, chatID)
	} else if strings.HasPrefix(callbackData, "override_select_") {
		// Выбран критерий для переопределения
		criterionName := strings.TrimPrefix(callbackData, "override_select_")
		showCriterionOverrideOptions(bot, chatID, criterionName)
	} else if callbackData == "override_done" {
		// Пользователь закончил переопределение весов
		state.Step = 6
		calcAndShowResult(bot, chatID)
	} else if callbackData == "override_cancel" {
		// Отмена переопределения текущего критерия
		showOverrideCriteriaList(bot, chatID)
	} else if strings.HasPrefix(callbackData, "weight_") {
		// Обработка выбора веса
		parts := strings.Split(callbackData, "_")
		if len(parts) == 3 {
			step, _ := strconv.Atoi(parts[1])
			value, _ := strconv.Atoi(parts[2])

			// Обновляем временное значение
			switch step {
			case 0:
				state.TempOverride.OnPrem = value
			case 1:
				state.TempOverride.Private = value
			case 2:
				state.TempOverride.Public = value
			}

			// Переходим к следующему шагу или сохраняем и возвращаемся к списку
			if step < 2 {
				state.OverrideStep++
				showWeightOptions(bot, chatID, state.CurrentOverride)
			} else {
				// Сохраняем финальные значения
				state.OverriddenScores[state.CurrentOverride] = state.TempOverride

				logger.LogTelegramAction("Баллы переопределены", map[string]interface{}{
					"Критерий": state.CurrentOverride,
					"OnPrem":   state.TempOverride.OnPrem,
					"Private":  state.TempOverride.Private,
					"Public":   state.TempOverride.Public,
				})

				// Возвращаемся к списку критериев
				showOverrideCriteriaList(bot, chatID)
			}
		}
	} else if callbackData == "override_no" {
		// Пользователь не хочет переопределять баллы
		state.Step = 6
		logger.LogTelegramAction("Отказ от переопределения баллов", nil)
		calcAndShowResult(bot, chatID)
	}
}

// Начинает процесс выбора приоритетов
func startPrioritySelection(bot *tgbotapi.BotAPI, chatID int64) {
	state := userStates[chatID]

	// Находим критерий, для которого еще не установлен приоритет
	var criterionToRate string
	for _, critName := range state.SelectedCriteria {
		if _, ok := state.CriteriaPriorities[critName]; !ok {
			criterionToRate = critName
			break
		}
	}

	if criterionToRate == "" {
		// Все приоритеты установлены
		return
	}

	// Находим описание критерия
	crit := findCriterionByName(criterionToRate)

	// Формируем сообщение
	text := fmt.Sprintf("Установите приоритет для критерия:\n\n*%s*\n%s",
		criterionToRate, crit.Description)

	var keyboardRow []tgbotapi.InlineKeyboardButton
	for i := 1; i <= 5; i++ {
		keyboardRow = append(keyboardRow,
			tgbotapi.NewInlineKeyboardButtonData(fmt.Sprintf("%d", i),
				fmt.Sprintf("prio_%s_%d", criterionToRate, i)))
	}

	keyboard := tgbotapi.NewInlineKeyboardMarkup(keyboardRow)

	logger.LogTelegramAction("Запрос приоритета", map[string]interface{}{
		"Критерий": criterionToRate,
	})

	// Если уже есть сообщение с приоритетами, обновляем его
	if state.PriorityMessageID != 0 {
		editMsg := tgbotapi.NewEditMessageTextAndMarkup(
			chatID,
			state.PriorityMessageID,
			text,
			keyboard,
		)
		editMsg.ParseMode = "Markdown"

		if _, err := editMessageText(bot, editMsg); err != nil {
			logger.Printf("Ошибка обновления сообщения приоритета: %v", err)
		}
	} else {
		// Иначе отправляем новое сообщение
		msg := tgbotapi.NewMessage(chatID, text)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = keyboard

		sentMsg, err := sendMessage(bot, msg)
		if err == nil {
			state.PriorityMessageID = sentMsg.MessageID
		} else {
			logger.Printf("Ошибка отправки сообщения с приоритетами: %v", err)
		}
	}
}

// Показывает варианты для специальных критериев
func showSpecialCriteriaOptions(bot *tgbotapi.BotAPI, chatID int64) {
	state := userStates[chatID]

	// Находим первый необработанный спец. критерий
	var criterionToSpecify string
	for _, critName := range state.SelectedCriteria {
		crit := findCriterionByName(critName)
		if crit.IsSpecial && state.SpecialValues[critName] == "" {
			criterionToSpecify = critName
			break
		}
	}

	if criterionToSpecify == "" {
		return
	}

	var options []string
	var callbackPrefix string
	var msgText string

	if criterionToSpecify == "Объём данных" {
		options = []string{"Малый", "Средний", "Большой"}
		callbackPrefix = "sdata" // Сокращение от "special_data"
		msgText = "Укажите объем данных:\n\n" +
			"• *Малый* — до 100 ГБ данных (несколько таблиц, тысячи-миллионы записей)\n" +
			"• *Средний* — от 100 ГБ до 1 ТБ (множество таблиц, миллионы-миллиарды записей)\n" +
			"• *Большой* — более 1 ТБ (сложная структура, миллиарды записей и выше)"
	} else if criterionToSpecify == "Срок использования" {
		options = []string{"Краткосрочный", "Долгосрочный"}
		callbackPrefix = "susage" // Сокращение от "special_usage"
		msgText = "Укажите планируемый срок использования:\n\n" +
			"• *Краткосрочный* — до 1-2 лет (временные проекты, эксперименты)\n" +
			"• *Долгосрочный* — от 3 лет и более (постоянные, долгосрочные системы)"
	} else {
		// Для других спец. критериев
		options = []string{"Опция 1", "Опция 2", "Опция 3"}
		callbackPrefix = "sother"
		msgText = fmt.Sprintf("Укажите значение для '%s':", criterionToSpecify)
	}

	logger.LogTelegramAction("Запрос специального значения", map[string]interface{}{
		"Критерий": criterionToSpecify,
		"Опции":    options,
	})

	// Создаем кнопки для каждого варианта с короткими callback_data
	var keyboardRows [][]tgbotapi.InlineKeyboardButton
	for i, option := range options {
		// Используем индекс для callback_data вместо полного текста
		callbackData := fmt.Sprintf("%s_%d", callbackPrefix, i)

		keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData(option, callbackData),
		})
	}

	// Создаем клавиатуру
	keyboard := tgbotapi.NewInlineKeyboardMarkup(keyboardRows...)

	// Сохраняем или обновляем сообщение
	if state.SpecialMessageID != 0 {
		editMsg := tgbotapi.NewEditMessageTextAndMarkup(
			chatID,
			state.SpecialMessageID,
			msgText,
			keyboard, // Напрямую передаем созданную клавиатуру
		)
		editMsg.ParseMode = "Markdown"

		if sent, err := editMessageText(bot, editMsg); err == nil {
			state.SpecialMessageID = sent.MessageID
		} else {
			logger.Printf("Ошибка обновления сообщения специальных значений: %v", err)
		}
	} else {
		msg := tgbotapi.NewMessage(chatID, msgText)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = keyboard

		if sent, err := sendMessage(bot, msg); err == nil {
			state.SpecialMessageID = sent.MessageID
		} else {
			logger.Printf("Ошибка отправки сообщения со специальными значениями: %v", err)
		}
	}
}

func showOverrideCriteriaList(bot *tgbotapi.BotAPI, chatID int64) {
	state := userStates[chatID]
	var keyboardRows [][]tgbotapi.InlineKeyboardButton

	// Добавляем выбранные критерии
	for _, critName := range state.SelectedCriteria {
		// Показываем статус (переопределено или нет)
		buttonText := critName
		if _, ok := state.OverriddenScores[critName]; ok {
			buttonText = "✓ " + buttonText // Уже переопределено
		}

		keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
			tgbotapi.NewInlineKeyboardButtonData(buttonText, "override_select_"+critName),
		})
	}

	// Добавляем кнопку "Готово"
	keyboardRows = append(keyboardRows, []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("✅ Готово", "override_done"),
	})

	keyboard := tgbotapi.NewInlineKeyboardMarkup(keyboardRows...)

	msg := tgbotapi.NewMessage(chatID, "Выберите критерий, для которого хотите изменить веса:")
	msg.ReplyMarkup = keyboard

	sentMsg, err := sendMessage(bot, msg)
	if err == nil && sentMsg.MessageID != 0 {
		state.OverrideMessageID = sentMsg.MessageID
	}
}

// Показывает текущие веса критерия и кнопки для их изменения
func showCriterionOverrideOptions(bot *tgbotapi.BotAPI, chatID int64, criterionName string) {
	state := userStates[chatID]

	// Получаем текущие веса
	crit := findCriterionByName(criterionName)
	scores := crit.BaseScores

	// Если есть переопределенные веса, используем их
	if overridden, ok := state.OverriddenScores[criterionName]; ok {
		scores = overridden
	}

	// Создаем временную копию для изменения
	state.TempOverride = scores
	state.CurrentOverride = criterionName
	state.OverrideStep = 0 // Начинаем с OnPrem

	// Отображаем текущие веса и запрашиваем изменение для OnPrem
	showWeightOptions(bot, chatID, criterionName)
}

// Показывает опции для изменения конкретного веса
func showWeightOptions(bot *tgbotapi.BotAPI, chatID int64, criterionName string) {
	state := userStates[chatID]

	var deploymentType string
	var currentValue int

	switch state.OverrideStep {
	case 0:
		deploymentType = "On-Premise"
		currentValue = state.TempOverride.OnPrem
	case 1:
		deploymentType = "Private Cloud"
		currentValue = state.TempOverride.Private
	case 2:
		deploymentType = "Public Cloud"
		currentValue = state.TempOverride.Public
	}

	// Формируем красивое сообщение с текущими весами
	msgText := fmt.Sprintf("Изменение весов для критерия *%s*\n\n", criterionName)
	msgText += fmt.Sprintf("*Текущие веса:*\n")
	msgText += fmt.Sprintf("• On-Premise: %d\n", state.TempOverride.OnPrem)
	msgText += fmt.Sprintf("• Private Cloud: %d\n", state.TempOverride.Private)
	msgText += fmt.Sprintf("• Public Cloud: %d\n\n", state.TempOverride.Public)

	msgText += fmt.Sprintf("Выберите новое значение для *%s*:", deploymentType)

	// Создаем кнопки от 1 до 10
	var rows [][]tgbotapi.InlineKeyboardButton
	var row []tgbotapi.InlineKeyboardButton

	for i := 1; i <= 10; i++ {
		buttonText := fmt.Sprintf("%d", i)
		if i == currentValue {
			buttonText = "• " + buttonText + " •" // Отмечаем текущее значение
		}

		callbackData := fmt.Sprintf("weight_%d_%d", state.OverrideStep, i)
		row = append(row, tgbotapi.NewInlineKeyboardButtonData(buttonText, callbackData))

		// По 5 кнопок в ряду
		if i%5 == 0 || i == 10 {
			rows = append(rows, row)
			row = []tgbotapi.InlineKeyboardButton{}
		}
	}

	// Добавляем кнопку Отмена
	rows = append(rows, []tgbotapi.InlineKeyboardButton{
		tgbotapi.NewInlineKeyboardButtonData("❌ Отмена", "override_cancel"),
	})

	keyboard := tgbotapi.NewInlineKeyboardMarkup(rows...)

	// Если уже есть сообщение для переопределения, обновляем его
	if state.OverrideMessageID != 0 {
		editMsg := tgbotapi.NewEditMessageTextAndMarkup(
			chatID,
			state.OverrideMessageID,
			msgText,
			keyboard,
		)
		editMsg.ParseMode = "Markdown"

		if _, err := editMessageText(bot, editMsg); err != nil {
			logger.Printf("Ошибка обновления сообщения для переопределения весов: %v", err)
		}
	} else {
		msg := tgbotapi.NewMessage(chatID, msgText)
		msg.ParseMode = "Markdown"
		msg.ReplyMarkup = keyboard

		sentMsg, err := sendMessage(bot, msg)
		if err == nil && sentMsg.MessageID != 0 {
			state.OverrideMessageID = sentMsg.MessageID
		}
	}
}

// Спрашивает, хочет ли пользователь переопределять баллы
func askOverride(bot *tgbotapi.BotAPI, chatID int64) {
	msg := tgbotapi.NewMessage(chatID, "Хотите ли переопределить базовые баллы (веса) для выбранных критериев?")

	keyboard := tgbotapi.NewInlineKeyboardMarkup(
		tgbotapi.NewInlineKeyboardRow(
			tgbotapi.NewInlineKeyboardButtonData("Да", "override_yes"),
			tgbotapi.NewInlineKeyboardButtonData("Нет", "override_no"),
		),
	)

	msg.ReplyMarkup = keyboard
	logger.LogTelegramAction("Запрос переопределения баллов", nil)
	sendMessage(bot, msg)
}

func processScoreOverride(bot *tgbotapi.BotAPI, chatID int64, text string) {
	state := userStates[chatID]

	// Удаляем возможные обратные кавычки и прочие спецсимволы
	text = strings.ReplaceAll(text, "`", "")

	logger.LogTelegramAction("Получены переопределенные баллы", map[string]interface{}{
		"Текст ввода": text,
	})

	successfulOverrides := 0

	// Разбираем ввод на части
	parts := strings.Split(text, "=")
	if len(parts) == 2 {
		// Обрабатываем одиночный ввод без запятых
		name := strings.TrimSpace(parts[0])
		scoresText := strings.TrimSpace(parts[1])
		scoresArr := strings.Split(scoresText, ",")

		if len(scoresArr) == 3 {
			onPremVal, err1 := strconv.Atoi(strings.TrimSpace(scoresArr[0]))
			privateVal, err2 := strconv.Atoi(strings.TrimSpace(scoresArr[1]))
			publicVal, err3 := strconv.Atoi(strings.TrimSpace(scoresArr[2]))

			if err1 == nil && err2 == nil && err3 == nil {
				// Для нечувствительного к регистру поиска критерия
				foundCriteria := false
				for _, crit := range defaultCriteria {
					if strings.EqualFold(crit.Name, name) {
						state.OverriddenScores[crit.Name] = Scores{OnPrem: onPremVal, Private: privateVal, Public: publicVal}
						successfulOverrides++
						logger.LogTelegramAction("Баллы переопределены", map[string]interface{}{
							"Критерий": crit.Name,
							"OnPrem":   onPremVal,
							"Private":  privateVal,
							"Public":   publicVal,
						})
						foundCriteria = true
						break
					}
				}

				if !foundCriteria {
					logger.LogTelegramAction("Критерий не найден", map[string]interface{}{
						"Введенное название": name,
					})
					msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("Критерий '%s' не найден. Проверьте название и попробуйте снова.", name))
					sendMessage(bot, msg)
					return
				}
			} else {
				logger.LogTelegramAction("Ошибка в формате баллов", map[string]interface{}{
					"Ввод": scoresText,
				})
				msg := tgbotapi.NewMessage(chatID, "Ошибка в формате баллов. Укажите три числа через запятую.")
				sendMessage(bot, msg)
				return
			}
		} else {
			// Обработка стандартного ввода с запятыми между парами критерий=баллы
			pairs := strings.Split(text, ",")
			for _, p := range pairs {
				p = strings.TrimSpace(p)
				pairParts := strings.Split(p, "=")
				if len(pairParts) != 2 {
					continue
				}

				// ... [существующий код обработки пар]
				// Аналогично добавить чувствительность к регистру и логирование
			}
		}
	}

	if successfulOverrides == 0 {
		msg := tgbotapi.NewMessage(chatID, "Не удалось переопределить баллы. Проверьте формат ввода и попробуйте снова.")
		sendMessage(bot, msg)
		return
	}

	state.Step = 6
	calcAndShowResult(bot, chatID)
}

// Собственно подсчёт результатов
func calcAndShowResult(bot *tgbotapi.BotAPI, chatID int64) {
	state := userStates[chatID]
	onPremTotal := 0
	privateTotal := 0
	publicTotal := 0

	// Формируем подробный отчет
	var detailsMsg strings.Builder
	detailsMsg.WriteString("Детализация расчета:\n\n")

	logger.LogTelegramAction("Начат расчет результатов", map[string]interface{}{
		"Выбрано критериев": len(state.SelectedCriteria),
		"С приоритетами":    len(state.CriteriaPriorities),
		"Переопределено":    len(state.OverriddenScores),
	})

	// Проходим по выбранным критериям
	for _, cName := range state.SelectedCriteria {
		crit := findCriterionByName(cName)
		prio := state.CriteriaPriorities[cName]
		if prio == 0 {
			prio = 1 // на всякий случай
		}

		// Получаем базовые или переопределённые баллы
		scores := crit.BaseScores
		source := "базовый"

		// Если критерий специальный, то подставляем баллы
		if crit.IsSpecial {
			val := state.SpecialValues[cName]
			scores = getScoresForSpecialCriterion(crit.Name, val)
			source = fmt.Sprintf("специальный (%s)", val)
		}

		// Если есть переопределенные баллы, то берем их
		if overridden, ok := state.OverriddenScores[cName]; ok {
			scores = overridden
			source = "переопределенный"
		}

		// Подсчитываем общие баллы
		onPremTotal += scores.OnPrem * prio
		privateTotal += scores.Private * prio
		publicTotal += scores.Public * prio

		// Добавляем в детализацию
		detailsMsg.WriteString(fmt.Sprintf("Критерий: %s\n", cName))
		detailsMsg.WriteString(fmt.Sprintf("  Приоритет: %d\n", prio))
		detailsMsg.WriteString(fmt.Sprintf("  Баллы (%s): OnPrem=%d, Private=%d, Public=%d\n",
			source, scores.OnPrem, scores.Private, scores.Public))
		detailsMsg.WriteString(fmt.Sprintf("  С учетом приоритета: OnPrem=%d, Private=%d, Public=%d\n\n",
			scores.OnPrem*prio, scores.Private*prio, scores.Public*prio))
	}

	// Определяем победителя
	resultMsg := fmt.Sprintf(
		"Итоговые баллы:\nOn-Premise: %d\nPrivate Cloud: %d\nPublic Cloud: %d\n\n",
		onPremTotal, privateTotal, publicTotal,
	)

	var recommendation string
	if onPremTotal > privateTotal && onPremTotal > publicTotal {
		recommendation = "On-Premise"
		resultMsg += "Рекомендуется On-Premise."
	} else if privateTotal > onPremTotal && privateTotal > publicTotal {
		recommendation = "Private Cloud"
		resultMsg += "Рекомендуется Private Cloud."
	} else if publicTotal > onPremTotal && publicTotal > privateTotal {
		recommendation = "Public Cloud"
		resultMsg += "Рекомендуется Public Cloud."
	} else {
		recommendation = "Требуется дополнительная оценка"
		resultMsg += "Варианты равны по баллам, нужна дополнительная оценка."
	}

	logger.LogTelegramAction("Результаты расчета", map[string]interface{}{
		"OnPrem":        onPremTotal,
		"Private":       privateTotal,
		"Public":        publicTotal,
		"Рекомендуется": recommendation,
	})

	// Отправляем результат
	sendMessage(bot, tgbotapi.NewMessage(chatID, resultMsg))

	// Отправляем детализацию
	sendMessage(bot, tgbotapi.NewMessage(chatID, detailsMsg.String()))

	aiAnalysis, err := getAISuggestions(filterString(detailsMsg.String(), "Баллы", "С учетом приоритета:"))
	if err != nil {
		logger.Printf("Ошибка получения анализа AI: %v", err)
		// В случае ошибки все равно завершаем без AI рекомендации
	} else {
		// При успешном получении анализа
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("*Рекомендация AI*: %s", aiAnalysis))
		msg.ParseMode = "Markdown"
		sendMessage(bot, msg)
	}

	// Предлагаем начать заново
	msg := tgbotapi.NewMessage(chatID, "Чтобы начать новый чеклист, \n введите /start")
	sendMessage(bot, msg)
}

// Функция возвращает критерий по названию
func findCriterionByName(name string) Criterion {
	for _, c := range defaultCriteria {
		if c.Name == name {
			return c
		}
	}
	return Criterion{}
}

// Функция для расчёта баллов спецкритериев
func getScoresForSpecialCriterion(name, userValue string) Scores {
	switch name {
	case "Объём данных":
		lower := strings.ToLower(userValue)
		switch lower {
		case "малый":
			// Малый объем данных (до 100 ГБ):
			// OnPrem - хорошо справляется с малыми объемами
			// Private - тоже хорошо подходит
			// Public - наиболее эффективен, не требуется покупка большого оборудования
			return Scores{OnPrem: 8, Private: 7, Public: 9}
		case "средний":
			// Средний объем (100 ГБ - 1 ТБ):
			// OnPrem - начинаются проблемы с масштабированием
			// Private - хорошо справляется
			// Public - наилучший вариант по соотношению цена/производительность
			return Scores{OnPrem: 6, Private: 8, Public: 9}
		case "большой":
			// Большой объем (более 1 ТБ):
			// OnPrem - высокие затраты на оборудование, проблемы с масштабированием
			// Private - хорошо справляется с большими данными
			// Public - предлагает лучшие возможности по масштабированию
			return Scores{OnPrem: 4, Private: 8, Public: 9}
		default:
			return Scores{OnPrem: 5, Private: 5, Public: 5}
		}
	case "Срок использования":
		lower := strings.ToLower(userValue)
		switch lower {
		case "краткосрочный":
			// Краткосрочный (до 1-2 лет):
			// OnPrem - высокие начальные инвестиции не окупаются
			// Private - требует значительной настройки
			// Public - оптимален для быстрого старта и краткосрочных проектов
			return Scores{OnPrem: 4, Private: 6, Public: 9}
		case "долгосрочный":
			// Долгосрочный (3+ лет):
			// OnPrem - начальные инвестиции окупаются со временем
			// Private - хорошая долгосрочная стратегия
			// Public - может быть дороже в долгосрочной перспективе
			return Scores{OnPrem: 9, Private: 7, Public: 6}
		default:
			return Scores{OnPrem: 5, Private: 5, Public: 5}
		}
	default:
		return Scores{OnPrem: 0, Private: 0, Public: 0}
	}
}

// Вспомогательная функция для проверки, содержится ли элемент в срезе
func contains(arr []string, val string) bool {
	for _, v := range arr {
		if v == val {
			return true
		}
	}
	return false
}

// Создаем свои типы для логирования
type CustomLogger struct {
	debug bool
	out   io.Writer
}

func NewLogger(debug bool) *CustomLogger {
	return &CustomLogger{
		debug: debug,
		out:   os.Stdout,
	}
}

func (l *CustomLogger) Printf(format string, v ...interface{}) {
	if !l.debug {
		return
	}

	timestamp := time.Now().Format("15:04:05")
	fmt.Fprintf(l.out, "[%s] ", timestamp)
	fmt.Fprintf(l.out, format+"\n", v...)
}

// Функция для перехвата и преобразования логов
func (l *CustomLogger) LogTelegramAction(action string, msg interface{}) {
	if !l.debug {
		return
	}

	timestamp := time.Now().Format("15:04:05")

	// Преобразуем объект в JSON
	jsonData, err := json.MarshalIndent(msg, "", "  ")
	if err != nil {
		l.Printf("[%s] Ошибка логирования: %v", timestamp, err)
		return
	}

	// Выводим красивый и читаемый лог
	fmt.Fprintf(l.out, "[%s] === %s ===\n%s\n\n", timestamp, action, string(jsonData))
}

// Функция-обертка для отправки сообщений с логированием
func sendMessage(bot *tgbotapi.BotAPI, msg tgbotapi.MessageConfig) (tgbotapi.Message, error) {
	logger.LogTelegramAction("Отправка сообщения", map[string]interface{}{
		"Чат":    msg.ChatID,
		"Текст":  msg.Text,
		"Кнопки": msg.ReplyMarkup != nil,
	})

	return bot.Send(msg)
}

// Обертки для других методов
func editMessageText(bot *tgbotapi.BotAPI, msg tgbotapi.EditMessageTextConfig) (tgbotapi.Message, error) {
	logger.LogTelegramAction("Редактирование сообщения", map[string]interface{}{
		"Чат":       msg.ChatID,
		"Сообщение": msg.MessageID,
		"Текст":     msg.Text,
	})

	return bot.Send(msg)
}

func editMessageReplyMarkup(bot *tgbotapi.BotAPI, msg tgbotapi.EditMessageReplyMarkupConfig) (tgbotapi.Message, error) {
	logger.LogTelegramAction("Обновление кнопок", map[string]interface{}{
		"Чат":       msg.ChatID,
		"Сообщение": msg.MessageID,
	})

	return bot.Send(msg)
}

func logCallbackQuery(query *tgbotapi.CallbackQuery) {
	logger.LogTelegramAction("Callback запрос", map[string]interface{}{
		"ID":        query.ID,
		"От":        query.From.UserName,
		"Данные":    query.Data,
		"Сообщение": query.Message.MessageID,
		"Чат":       query.Message.Chat.ID,
	})
}

func getAISuggestions(details string) (string, error) {
	client := yandexgpt.NewYandexGPTClientWithAPIKey("AQVN2q27j-dqYFXE9n9lx15QtklR9N7sXeO9om0H")
	request := yandexgpt.YandexGPTRequest{
		ModelURI: yandexgpt.MakeModelURI("b1gntlqp077vnspfnjhf", yandexgpt.YandexGPT4Model32k),
		CompletionOptions: yandexgpt.YandexGPTCompletionOptions{
			Stream:      false,
			Temperature: 0.7,
			MaxTokens:   2000,
		},
		Messages: []yandexgpt.YandexGPTMessage{
			{
				Role: yandexgpt.YandexGPTMessageRoleSystem,
				Text: descriptionLLM,
			},
			{
				Role: yandexgpt.YandexGPTMessageRoleUser,
				Text: fmt.Sprintf("Вот какие критерии и приоритеты выбрал директор компании: %s", details),
			},
		},
	}

	response, err := client.GetCompletion(context.Background(), request)
	if err != nil {
		return "", err
	}

	return response.Result.Alternatives[0].Message.Text, nil
}

const descriptionLLM = `
Начальник компания прошел тест на выбор типа СУБД: On-Premise, Private Cloud, Public Cloud. Он выбрал только нужные из списка критерии и расставил приоритеты от 1 до 5. Где 1 - самый низкая значимость критерия, 5 самая высокая значимость.
Список критериев: "Юрисдикция данных", "Отраслевые стандарты", "Физическая безопасность", "Объём данных", "Латентность", "Вариативность нагрузки", "Начальные инвестиции", "Постоянные затраты, 
"Срок использования", "Квалификация персонала", "Время до запуска", "Масштабируемость". Сейчас тебе напишут, какие критерии директор выбрал и с какими приоритетами, ты должна проанализировать выбранные критерии и их приоритеты. 
Необходимо определить какой из 3х типов СУБД: On-Premise, Private Cloud, Public Cloud лучше подходит компании и почему. Формат ответа должен быть таким: <On-Premise/Private Cloud/Public Cloud> \n Обоснование: ...`

func filterString(input string, patterns ...string) string {
	var result []string

	// Разделяем входную строку на отдельные строки по символу новой строки
	lines := strings.Split(input, "\n")

	// Проходим по каждой строке
	for _, line := range lines {
		containsPattern := false

		// Проверяем, содержит ли строка какой-либо из паттернов
		for _, pattern := range patterns {
			if strings.Contains(line, pattern) {
				containsPattern = true
				break
			}
		}

		// Если строка не содержит паттерн, добавляем её в результат
		if !containsPattern {
			result = append(result, line)
		}
	}

	// Объединяем отфильтрованные строки обратно в одну строку
	return strings.Join(result, "\n")
}
