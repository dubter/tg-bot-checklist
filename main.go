package main

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	// Импортируем PostgreSQL драйвер
	_ "github.com/lib/pq" // Обратите внимание на "_" - драйвер регистрируется, но не используется напрямую

	"github.com/sheeiavellie/go-yandexgpt"

	tgbotapi "github.com/go-telegram-bot-api/telegram-bot-api/v5"
)

// Scores хранит веса для каждого варианта развёртывания.
type Scores struct {
	OnPrem  int `json:"on_prem"` // Добавляем теги json для сериализации
	Private int `json:"private"`
	Public  int `json:"public"`
}

// Criterion описывает один критерий.
type Criterion struct {
	Name        string `json:"name"`
	Category    string `json:"category"`
	BaseScores  Scores `json:"base_scores"`
	Description string `json:"description"`
	IsSpecial   bool   `json:"is_special"` // если true, нужно уточнять у пользователя конкретный вариант
}

// UserInputData структура для хранения всех пользовательских вводов
type UserInputData struct {
	CriteriaPriorities map[string]int    `json:"criteria_priorities"`
	OverriddenScores   map[string]Scores `json:"overridden_scores"`
	SpecialValues      map[string]string `json:"special_values"`
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
	host            = "rc1d-7vowbk5nhczg7plw.mdb.yandexcloud.net"
	port            = 6432
	user            = "mvpshe"
	password        = os.Getenv("DB_PASSWORD")
	dbname          = "db"
	ca              = "/etc/ssl/certs/root.crt"
	userStates      = make(map[int64]*UserState) // key = chatID
	defaultCriteria = getDefaultCriteria()
	logger          *CustomLogger
	conn            *pgx.Conn
)

// Инициализация критериев
func getDefaultCriteria() []Criterion {
	// ... (остальная часть функции без изменений)
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

// Функция для инициализации подключения к БД
func initDB() {
	rootCertPool := x509.NewCertPool()
	pem, err := os.ReadFile(ca)
	if err != nil {
		panic(err)
	}

	if ok := rootCertPool.AppendCertsFromPEM(pem); !ok {
		panic("Failed to append PEM.")
	}

	connString := fmt.Sprintf(
		"host=%s port=%d dbname=%s user=%s password=%s sslmode=verify-full target_session_attrs=read-write",
		host, port, dbname, user, password)

	connConfig, err := pgx.ParseConfig(connString)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to parse config: %v\n", err)
		os.Exit(1)
	}

	connConfig.TLSConfig = &tls.Config{
		RootCAs:            rootCertPool,
		InsecureSkipVerify: true,
	}

	conn, err = pgx.ConnectConfig(context.Background(), connConfig)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Unable to connect to database: %v\n", err)
		os.Exit(1)
	}

	// Проверяем соединение
	err = conn.Ping(context.Background())
	if err != nil {
		logger.Printf("Ошибка проверки соединения с БД: %v", err)
		log.Fatalf("Не удалось проверить соединение с базой данных: %v", err)
	}

	logger.Printf("Успешно подключено к базе данных.")

	// Опционально: Создаем таблицу, если её нет
	createTableSQL := `
	CREATE TABLE IF NOT EXISTS answers (
		id SERIAL PRIMARY KEY,
		user_id BIGINT NOT NULL,
		user_input JSONB, -- Храним всю информацию о введенных данных пользователя в JSON
		algorithm_result TEXT,
		gpt_answer TEXT,
		match BOOLEAN,
		created_at TIMESTAMP WITH TIME ZONE DEFAULT CURRENT_TIMESTAMP
	);`

	_, err = conn.Exec(context.Background(), createTableSQL)
	if err != nil {
		logger.Printf("Ошибка создания таблицы 'answers': %v", err)
		log.Fatalf("Не удалось создать таблицу 'answers': %v", err)
	} else {
		logger.Printf("Таблица 'answers' успешно проверена/создана.")
	}
}

func main() {
	logger = NewLogger(true) // Инициализируем логгер до БД

	// Проверяем наличие переменных окружения
	if botToken == "" {
		log.Fatal("Переменная окружения BOT_TOKEN не установлена.")
	}
	if password == "" {
		log.Fatal("Переменная окружения DB_PASSWORD не установлена.")
	}

	initDB()                               // Инициализируем подключение к БД
	defer conn.Close(context.Background()) // Закрываем подключение при завершении программы

	bot, err := tgbotapi.NewBotAPI(botToken)
	if err != nil {
		logger.Printf("Ошибка инициализации бота: %v", err)
		log.Panic(err)
	}

	// ... (остальная часть функции main без изменений)
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
				// Обработка ввода переопределенных баллов - Эта логика устарела с введением кнопок
				// processScoreOverride(bot, chatID, text)
				// Нужно сообщить пользователю, что ввод текста больше не поддерживается
				msg := tgbotapi.NewMessage(chatID, "Пожалуйста, используйте кнопки для переопределения весов.")
				sendMessage(bot, msg)
				showOverrideCriteriaList(bot, chatID) // Показать кнопки снова
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

			// Проверяем наличие state перед использованием
			if userStates[chatID] == nil {
				logger.Printf("Состояние пользователя для chatID %d не найдено!", chatID)
				// Можно отправить сообщение об ошибке или сбросить состояние
				msg := tgbotapi.NewMessage(chatID, "Произошла ошибка состояния. Пожалуйста, начните заново с /start.")
				sendMessage(bot, msg)
				continue // Переходим к следующему update
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

// Собственно подсчёт результатов и сохранение в БД
func calcAndShowResult(bot *tgbotapi.BotAPI, chatID int64) {
	state := userStates[chatID]
	if state == nil {
		logger.Printf("Критическая ошибка: state = nil в calcAndShowResult для chatID %d", chatID)
		return // Не можем продолжать без состояния
	}
	onPremTotal := 0
	privateTotal := 0
	publicTotal := 0

	// Формируем подробный отчет
	var detailsMsg strings.Builder
	detailsMsg.WriteString("Детализация расчета:\n\n")

	logger.LogTelegramAction("Начат расчет результатов", map[string]interface{}{
		"ChatID":            chatID,
		"Выбрано критериев": len(state.SelectedCriteria),
		"С приоритетами":    len(state.CriteriaPriorities),
		"Переопределено":    len(state.OverriddenScores),
		"Спец. значения":    state.SpecialValues,
	})

	// Собираем все введенные пользователем данные
	userInput := UserInputData{
		CriteriaPriorities: state.CriteriaPriorities,
		OverriddenScores:   state.OverriddenScores,
		SpecialValues:      state.SpecialValues,
	}

	// Проходим по выбранным критериям
	for _, cName := range state.SelectedCriteria {
		crit := findCriterionByName(cName)
		prio, prioOk := state.CriteriaPriorities[cName]
		if !prioOk {
			logger.Printf("Внимание: не найден приоритет для критерия '%s' у пользователя %d. Используется 1.", cName, chatID)
			prio = 1 // Дефолтный приоритет, если что-то пошло не так
		}

		// Получаем базовые или переопределённые баллы
		scores := crit.BaseScores
		source := "базовый"

		// Если критерий специальный, то подставляем баллы
		if crit.IsSpecial {
			val, valOk := state.SpecialValues[cName]
			if !valOk {
				logger.Printf("Внимание: не найдено специальное значение для критерия '%s' у пользователя %d. Используются дефолтные баллы.", cName, chatID)
				// Не меняем scores, останутся базовые нули или последние переопределенные
			} else {
				scores = getScoresForSpecialCriterion(crit.Name, val)
				source = fmt.Sprintf("специальный (%s)", val)
			}
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
		// Обработка равенства баллов
		equalOptions := []string{}
		maxScore := 0
		if onPremTotal >= maxScore {
			maxScore = onPremTotal
		}
		if privateTotal >= maxScore {
			maxScore = privateTotal
		}
		if publicTotal >= maxScore {
			maxScore = publicTotal
		}

		if onPremTotal == maxScore {
			equalOptions = append(equalOptions, "On-Premise")
		}
		if privateTotal == maxScore {
			equalOptions = append(equalOptions, "Private Cloud")
		}
		if publicTotal == maxScore {
			equalOptions = append(equalOptions, "Public Cloud")
		}

		if len(equalOptions) > 1 {
			recommendation = "Требуется дополнительная оценка (" + strings.Join(equalOptions, "/") + ")"
			resultMsg += "Варианты (" + strings.Join(equalOptions, ", ") + ") равны по баллам, нужна дополнительная оценка."
		} else {
			// Эта ветка не должна выполниться при текущей логике, но на всякий случай
			recommendation = "Требуется дополнительная оценка"
			resultMsg += "Не удалось однозначно определить лучший вариант, нужна дополнительная оценка."
		}
	}

	logger.LogTelegramAction("Результаты расчета", map[string]interface{}{
		"ChatID":        chatID,
		"OnPrem":        onPremTotal,
		"Private":       privateTotal,
		"Public":        publicTotal,
		"Рекомендуется": recommendation,
	})

	// Отправляем результат
	sendMessage(bot, tgbotapi.NewMessage(chatID, resultMsg))

	// Отправляем детализацию
	sendMessage(bot, tgbotapi.NewMessage(chatID, detailsMsg.String()))

	// --- Получение рекомендации AI ---
	aiAnalysis := "" // Инициализируем пустой строкой
	var aiErr error
	filteredDetails := filterString(detailsMsg.String(), "Баллы", "С учетом приоритета:") // Фильтруем для LLM
	aiAnalysis, aiErr = getAISuggestions(filteredDetails)                                 // Передаем отфильтрованные детали
	if aiErr != nil {
		logger.Printf("Ошибка получения анализа AI для chatID %d: %v", chatID, aiErr)
		sendMessage(bot, tgbotapi.NewMessage(chatID, "Не удалось получить рекомендацию от AI."))
		// Не прерываем выполнение, сохраним без AI ответа или с пустым полем
	} else {
		// При успешном получении анализа
		msg := tgbotapi.NewMessage(chatID, fmt.Sprintf("*Рекомендация AI*:\n%s", aiAnalysis)) // Добавил перенос строки для читаемости
		msg.ParseMode = "Markdown"
		sendMessage(bot, msg)
	}

	// --- Сохранение в базу данных ---
	match := false
	// Простая проверка совпадения (можно усложнить при необходимости)
	// Ищем название рекомендованного варианта (без доп. текста) в ответе AI
	simpleRecommendation := strings.Split(recommendation, " ")[0] // Берем первое слово ("On-Premise", "Private", "Public")
	if strings.Contains(strings.ToLower(aiAnalysis), strings.ToLower(simpleRecommendation)) {
		match = true
	}
	// Если алгоритм не дал однозначного ответа, считаем несовпадением
	if strings.HasPrefix(recommendation, "Требуется") {
		match = false
	}

	// Сериализуем пользовательский ввод в JSON
	userInputJSON, err := json.Marshal(userInput)
	if err != nil {
		logger.Printf("Ошибка сериализации userInput в JSON для chatID %d: %v", chatID, err)
		// Продолжаем без сохранения или сохраняем с NULL? Решаем сохранить без userInput.
		userInputJSON = []byte("null") // Или пустой объект {} ? Лучше null, чтобы показать ошибку
	}

	// Подготовка и выполнение SQL запроса
	insertSQL := `INSERT INTO answers (user_id, user_input, algorithm_result, gpt_answer, match) VALUES ($1, $2, $3, $4, $5)`
	_, err = conn.Exec(context.Background(), insertSQL, chatID, userInputJSON, recommendation, aiAnalysis, match)
	if err != nil {
		logger.Printf("Ошибка сохранения результата в БД для chatID %d: %v", chatID, err)
		sendMessage(bot, tgbotapi.NewMessage(chatID, "Произошла ошибка при сохранении результатов."))
	} else {
		logger.LogTelegramAction("Результат сохранен в БД", map[string]interface{}{
			"ChatID":           chatID,
			"AlgorithmResult":  recommendation,
			"GPTAnswerPresent": aiAnalysis != "",
			"Match":            match,
		})
	}
	// --- Конец сохранения в базу данных ---

	// Предлагаем начать заново
	msg := tgbotapi.NewMessage(chatID, "Чтобы начать новый чеклист, введите /start")
	sendMessage(bot, msg)

	// Очищаем состояние пользователя после завершения
	delete(userStates, chatID)
	logger.Printf("Состояние пользователя для chatID %d очищено.", chatID)
}

// ... (функции findCriterionByName, getScoresForSpecialCriterion, contains, CustomLogger, NewLogger, Printf, LogTelegramAction, sendMessage, editMessageText, editMessageReplyMarkup, logCallbackQuery, getAISuggestions, descriptionLLM, filterString - без изменений)

// --- Вспомогательные функции (без изменений) ---

// Функция возвращает критерий по названию
func findCriterionByName(name string) Criterion {
	for _, c := range defaultCriteria {
		if c.Name == name {
			return c
		}
	}
	// Возвращаем пустой Criterion, если не найден (нужно обрабатывать в вызывающем коде)
	logger.Printf("Внимание: Критерий с именем '%s' не найден в defaultCriteria.", name)
	return Criterion{}
}

// Функция для расчёта баллов спецкритериев
func getScoresForSpecialCriterion(name, userValue string) Scores {
	switch name {
	case "Объём данных":
		lower := strings.ToLower(userValue)
		switch lower {
		case "малый":
			return Scores{OnPrem: 8, Private: 7, Public: 9}
		case "средний":
			return Scores{OnPrem: 6, Private: 8, Public: 9}
		case "большой":
			return Scores{OnPrem: 4, Private: 8, Public: 9}
		default:
			logger.Printf("Неизвестное значение '%s' для спец. критерия '%s'. Возвращены дефолтные баллы.", userValue, name)
			return Scores{OnPrem: 5, Private: 5, Public: 5} // Дефолтные баллы при неизвестном значении
		}
	case "Срок использования":
		lower := strings.ToLower(userValue)
		switch lower {
		case "краткосрочный":
			return Scores{OnPrem: 4, Private: 6, Public: 9}
		case "долгосрочный":
			return Scores{OnPrem: 9, Private: 7, Public: 6}
		default:
			logger.Printf("Неизвестное значение '%s' для спец. критерия '%s'. Возвращены дефолтные баллы.", userValue, name)
			return Scores{OnPrem: 5, Private: 5, Public: 5} // Дефолтные баллы при неизвестном значении
		}
		// Добавьте сюда логику для других IsSpecial критериев, если они появятся
	default:
		logger.Printf("Попытка получить баллы для неизвестного спец. критерия '%s'.", name)
		return Scores{OnPrem: 0, Private: 0, Public: 0} // Нулевые баллы для неизвестного критерия
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

// --- Логирование и обертки для Telegram API (без изменений) ---
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
	timestamp := time.Now().Format("15:04:05.000") // Добавил миллисекунды для точности
	fmt.Fprintf(l.out, "[%s] ", timestamp)
	fmt.Fprintf(l.out, format+"\n", v...)
}

func (l *CustomLogger) LogTelegramAction(action string, msg interface{}) {
	if !l.debug {
		return
	}
	timestamp := time.Now().Format("15:04:05.000")

	// Проверяем, что msg не nil перед маршалингом
	var jsonData []byte
	var err error
	if msg != nil {
		jsonData, err = json.MarshalIndent(msg, "", "  ")
		if err != nil {
			l.Printf("[%s] Ошибка логирования (маршалинг JSON): %v. Данные: %+v", timestamp, err, msg)
			// Выводим хотя бы базовую информацию, если JSON не удался
			fmt.Fprintf(l.out, "[%s] === %s ===\n%+v\n\n", timestamp, action, msg)
			return
		}
	} else {
		jsonData = []byte("(нет данных)")
	}

	fmt.Fprintf(l.out, "[%s] === %s ===\n%s\n\n", timestamp, action, string(jsonData))
}

func sendMessage(bot *tgbotapi.BotAPI, msg tgbotapi.MessageConfig) (tgbotapi.Message, error) {
	logger.LogTelegramAction("Отправка сообщения", map[string]interface{}{
		"ChatID":      msg.ChatID,
		"Text":        msg.Text, // Обрезаем длинный текст для лога, если нужно
		"ParseMode":   msg.ParseMode,
		"HasKeyboard": msg.ReplyMarkup != nil,
	})
	sentMsg, err := bot.Send(msg)
	if err != nil {
		logger.Printf("Ошибка отправки сообщения в чат %d: %v", msg.ChatID, err)
	}
	return sentMsg, err
}

func editMessageText(bot *tgbotapi.BotAPI, msg tgbotapi.EditMessageTextConfig) (tgbotapi.Message, error) {
	logger.LogTelegramAction("Редактирование текста сообщения", map[string]interface{}{
		"ChatID":    msg.ChatID,
		"MessageID": msg.MessageID,
		"Text":      msg.Text, // Обрезаем длинный текст для лога, если нужно
		"ParseMode": msg.ParseMode,
	})
	sentMsg, err := bot.Send(msg)
	if err != nil {
		logger.Printf("Ошибка редактирования текста сообщения %d в чате %d: %v", msg.MessageID, msg.ChatID, err)
	}
	return sentMsg, err
}

func editMessageReplyMarkup(bot *tgbotapi.BotAPI, msg tgbotapi.EditMessageReplyMarkupConfig) (tgbotapi.Message, error) {
	logger.LogTelegramAction("Обновление кнопок сообщения", map[string]interface{}{
		"ChatID":    msg.ChatID,
		"MessageID": msg.MessageID,
		// Не логгируем сами кнопки, чтобы не засорять лог
	})
	sentMsg, err := bot.Send(msg)
	if err != nil {
		logger.Printf("Ошибка обновления кнопок сообщения %d в чате %d: %v", msg.MessageID, msg.ChatID, err)
	}
	return sentMsg, err
}

func logCallbackQuery(query *tgbotapi.CallbackQuery) {
	// Проверяем, что query и query.Message не nil
	if query == nil {
		logger.Printf("Ошибка: получен nil CallbackQuery")
		return
	}
	var chatID int64
	var messageID int
	if query.Message != nil {
		chatID = query.Message.Chat.ID
		messageID = query.Message.MessageID
	} else {
		logger.Printf("Внимание: CallbackQuery без Message (%s)", query.ID)
	}

	logger.LogTelegramAction("Callback запрос", map[string]interface{}{
		"CallbackID": query.ID,
		"From":       query.From.UserName,
		"ChatID":     chatID,
		"MessageID":  messageID,
		"Data":       query.Data,
		"InlineMID":  query.InlineMessageID, // Добавлено для inline-режима, если используется
	})
}

// --- Взаимодействие с Yandex GPT (без изменений) ---
func getAISuggestions(details string) (string, error) {
	// Используйте актуальный способ получения ключа/IAM-токена
	apiKey := os.Getenv("YANDEX_API_KEY") // или IAM-токен
	folderID := os.Getenv("YANDEX_FOLDER_ID")

	if apiKey == "" || folderID == "" {
		return "", fmt.Errorf("Yandex API Key или Folder ID не установлены в переменных окружения")
	}

	// Используем клиент с API ключом. Если нужен IAM-токен, используйте соответствующий конструктор
	client := yandexgpt.NewYandexGPTClientWithAPIKey(apiKey)

	request := yandexgpt.YandexGPTRequest{
		ModelURI: yandexgpt.MakeModelURI(folderID, yandexgpt.YandexGPT4Model32k), // Укажите ваш folderID
		CompletionOptions: yandexgpt.YandexGPTCompletionOptions{
			Stream:      false,
			Temperature: 0.6,  // Немного уменьшил температуру для большей предсказуемости
			MaxTokens:   1500, // Уменьшил макс. токены, т.к. ответ не должен быть огромным
		},
		Messages: []yandexgpt.YandexGPTMessage{
			{
				Role: yandexgpt.YandexGPTMessageRoleSystem,
				Text: descriptionLLM, // Используем константу
			},
			{
				Role: yandexgpt.YandexGPTMessageRoleUser,
				Text: fmt.Sprintf("Вот какие критерии и приоритеты выбрал пользователь: \n%s", details), // Передаем отфильтрованные данные
			},
		},
	}

	logger.LogTelegramAction("Запрос к Yandex GPT", map[string]interface{}{
		"Model":       request.ModelURI,
		"Temperature": request.CompletionOptions.Temperature,
		"MaxTokens":   request.CompletionOptions.MaxTokens,
		"Prompt (начало)": func() string { // Логгируем начало промпта
			if len(request.Messages) > 1 {
				txt := request.Messages[1].Text
				if len(txt) > 100 {
					return txt[:100] + "..."
				}
				return txt
			}
			return ""
		}(),
	})

	response, err := client.GetCompletion(context.Background(), request)
	if err != nil {
		logger.Printf("Ошибка при запросе к Yandex GPT: %v", err)
		return "", fmt.Errorf("ошибка при обращении к Yandex GPT: %w", err)
	}

	aiText := response.Result.Alternatives[0].Message.Text
	logger.LogTelegramAction("Ответ от Yandex GPT получен", map[string]interface{}{
		"Response (начало)": func() string {
			if len(aiText) > 100 {
				return aiText[:100] + "..."
			}
			return aiText
		}(),
	})

	return aiText, nil
}

const descriptionLLM = `
Ты — эксперт по выбору инфраструктурных решений для баз данных. К тебе обращается пользователь, который прошел тест для определения оптимального типа развертывания СУБД: On-Premise, Private Cloud или Public Cloud.
Пользователь выбрал важные для него критерии из списка и установил их приоритет от 1 (низкий) до 5 (высокий).

Вот список всех возможных критериев:
- Юрисдикция данных: Насколько важна локализация данных и соответствие местным законам.
- Отраслевые стандарты: Требования к сертификации и соответствию отраслевым нормам (например, PCI DSS, HIPAA).
- Физическая безопасность: Насколько важно физическое расположение серверов и меры их защиты.
- Объём данных: Объем хранимых и обрабатываемых данных (Малый, Средний, Большой).
- Латентность: Требования к задержкам при доступе к данным.
- Вариативность нагрузки: Насколько часто и сильно меняется нагрузка на БД.
- Начальные инвестиции: Бюджет на первоначальное развертывание (оборудование, лицензии).
- Постоянные затраты: Регулярные расходы на поддержку, лицензии, электричество, персонал.
- Срок использования: Планируемый срок эксплуатации системы (Краткосрочный, Долгосрочный).
- Квалификация персонала: Наличие и уровень экспертизы команды по управлению БД и инфраструктурой.
- Время до запуска: Насколько быстро нужно развернуть систему.
- Масштабируемость: Требования к возможности быстрого увеличения или уменьшения ресурсов.

Тебе предоставят информацию о том, какие конкретно критерии выбрал пользователь, какие приоритеты он им назначил, и какие значения он указал для "специальных" критериев (Объём данных, Срок использования).

Твоя задача:
1. Проанализируй выбор пользователя: какие критерии для него наиболее важны (высокий приоритет), какие менее важны. Обрати внимание на комбинацию критериев.
2. На основе этого анализа дай **одну** четкую рекомендацию: какой из трех типов СУБД (**On-Premise**, **Private Cloud** или **Public Cloud**) лучше всего подходит для ситуации пользователя.
3. Предоставь краткое, но емкое **обоснование** своей рекомендации, объясняя, почему именно этот тип подходит лучше всего, исходя из приоритетов и выбора пользователя.

Формат ответа СТРОГО:
<On-Premise/Private Cloud/Public Cloud>
Обоснование: [Твое обоснование здесь]

Пример:
Public Cloud
Обоснование: Пользователь указал высокий приоритет для Масштабируемости и Времени до запуска, а также выбрал Краткосрочный срок использования. Public Cloud наилучшим образом удовлетворяет этим требованиям, позволяя быстро развернуть систему и гибко масштабировать ресурсы без значительных начальных инвестиций. Низкий приоритет Физической безопасности также делает Public Cloud приемлемым вариантом.
`

// --- Фильтрация строки для LLM (без изменений) ---
func filterString(input string, patterns ...string) string {
	var result []string
	lines := strings.Split(input, "\n")
	for _, line := range lines {
		// Пропускаем пустые строки и строку "Детализация расчета:"
		trimmedLine := strings.TrimSpace(line)
		if trimmedLine == "" || trimmedLine == "Детализация расчета:" {
			continue
		}

		containsPattern := false
		// Проверяем, содержит ли строка какой-либо из паттернов для исключения
		for _, pattern := range patterns {
			if strings.Contains(line, pattern) {
				containsPattern = true
				break
			}
		}
		// Если строка НЕ содержит паттерн, добавляем её в результат
		if !containsPattern {
			result = append(result, line)
		}
	}
	return strings.Join(result, "\n")
}
