package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"math/rand"
	"net/http"
	"os"
	"sync"
	"time"
)

type Scores struct {
	OnPrem  int `json:"on_prem"`
	Private int `json:"private"`
	Public  int `json:"public"`
}

type RecommendationRequest struct {
	SelectedCriteria   []string          `json:"selected_criteria"`
	CriteriaPriorities map[string]int    `json:"criteria_priorities"`
	SpecialValues      map[string]string `json:"special_values"`
}

type CriterionDetail struct {
	Name            string `json:"name"`
	Priority        int    `json:"priority"`
	Source          string `json:"source"`
	OnPremScore     int    `json:"on_prem_score"`
	PrivateScore    int    `json:"private_score"`
	PublicScore     int    `json:"public_score"`
	OnPremWeighted  int    `json:"on_prem_weighted"`
	PrivateWeighted int    `json:"private_weighted"`
	PublicWeighted  int    `json:"public_weighted"`
}

type RecommendationResponse struct {
	OnPremTotal    int               `json:"on_prem_total"`
	PrivateTotal   int               `json:"private_total"`
	PublicTotal    int               `json:"public_total"`
	Recommendation string            `json:"recommendation"`
	Details        []CriterionDetail `json:"details"`
	AIAnalysis     string            `json:"ai_analysis,omitempty"`
}

var allCriteria = []string{
	"Юрисдикция данных",
	"Отраслевые стандарты",
	"Физическая безопасность",
	"Объём данных",
	"Латентность",
	"Вариативность нагрузки",
	"Начальные инвестиции",
	"Постоянные затраты",
	"Срок использования",
	"Квалификация персонала",
	"Время до запуска",
	"Масштабируемость",
}

var specialCriteria = map[string][]string{
	"Объём данных":       {"Малый", "Средний", "Большой"},
	"Срок использования": {"Краткосрочный", "Долгосрочный"},
}

type RequestStats struct {
	Duration       time.Duration
	StatusCode     int
	Error          error
	Recommendation string
	OnPremTotal    int
	PrivateTotal   int
	PublicTotal    int
}

func main() {
	url := flag.String("url", "http://localhost:8080/api/recommend", "URL API рекомендаций")
	concurrency := flag.Int("c", 10, "Количество параллельных запросов")
	total := flag.Int("n", 100, "Общее количество запросов")
	delay := flag.Int("delay", 0, "Задержка между запросами в мс")
	outputFile := flag.String("o", "", "Файл для записи результатов (JSON)")
	verbose := flag.Bool("v", false, "Подробный вывод")
	flag.Parse()

	fmt.Printf("Начинаем нагрузочное тестирование API %s\n", *url)
	fmt.Printf("Параметры: %d запросов, %d параллельных потоков\n", *total, *concurrency)

	requests := generateRequests(*total)

	if *verbose {
		fmt.Printf("Сгенерировано %d уникальных тестовых запросов\n", len(requests))
	}

	stats := runLoadTest(requests, *url, *concurrency, *delay, *verbose)

	analyzeResults(stats, *verbose)

	if *outputFile != "" {
		saveResults(stats, *outputFile)
	}
}

func generateRequests(count int) []RecommendationRequest {
	rand.Seed(time.Now().UnixNano())
	requests := make([]RecommendationRequest, count)

	for i := 0; i < count; i++ {
		req := RecommendationRequest{
			CriteriaPriorities: make(map[string]int),
			SpecialValues:      make(map[string]string),
		}

		numCriteria := rand.Intn(len(allCriteria)-1) + 1
		shuffledCriteria := make([]string, len(allCriteria))
		copy(shuffledCriteria, allCriteria)
		rand.Shuffle(len(shuffledCriteria), func(i, j int) {
			shuffledCriteria[i], shuffledCriteria[j] = shuffledCriteria[j], shuffledCriteria[i]
		})

		req.SelectedCriteria = shuffledCriteria[:numCriteria]

		for _, criterion := range req.SelectedCriteria {
			req.CriteriaPriorities[criterion] = rand.Intn(5) + 1
		}

		for criterion, values := range specialCriteria {
			if contains(req.SelectedCriteria, criterion) {
				req.SpecialValues[criterion] = values[rand.Intn(len(values))]
			}
		}

		requests[i] = req
	}

	if count > 5 {
		allCriteriaReq := RecommendationRequest{
			SelectedCriteria:   allCriteria,
			CriteriaPriorities: make(map[string]int),
			SpecialValues:      make(map[string]string),
		}
		for _, criterion := range allCriteria {
			allCriteriaReq.CriteriaPriorities[criterion] = 3
		}
		for criterion, values := range specialCriteria {
			allCriteriaReq.SpecialValues[criterion] = values[0]
		}
		requests[0] = allCriteriaReq

		singleCriterionReq := RecommendationRequest{
			SelectedCriteria:   []string{"Юрисдикция данных"},
			CriteriaPriorities: map[string]int{"Юрисдикция данных": 5},
			SpecialValues:      make(map[string]string),
		}
		requests[1] = singleCriterionReq

		specialCriteriaReq := RecommendationRequest{
			SelectedCriteria:   []string{"Объём данных", "Срок использования"},
			CriteriaPriorities: make(map[string]int),
			SpecialValues:      make(map[string]string),
		}
		for _, criterion := range specialCriteriaReq.SelectedCriteria {
			specialCriteriaReq.CriteriaPriorities[criterion] = 4
			if values, ok := specialCriteria[criterion]; ok {
				specialCriteriaReq.SpecialValues[criterion] = values[len(values)-1]
			}
		}
		requests[2] = specialCriteriaReq
	}

	return requests
}

func runLoadTest(requests []RecommendationRequest, url string, concurrency, delay int, verbose bool) []RequestStats {
	totalRequests := len(requests)
	stats := make([]RequestStats, totalRequests)

	sem := make(chan bool, concurrency)
	var wg sync.WaitGroup

	fmt.Println("Запуск тестирования...")
	startTime := time.Now()

	for i, req := range requests {
		wg.Add(1)
		sem <- true

		go func(reqIndex int, request RecommendationRequest) {
			defer func() {
				<-sem
				wg.Done()
			}()

			stat := sendRequest(request, url, verbose)
			stats[reqIndex] = stat

			if verbose {
				fmt.Printf("Запрос %d/%d: %d мс, статус %d, рек.: %s\n",
					reqIndex+1, totalRequests,
					stat.Duration.Milliseconds(),
					stat.StatusCode,
					stat.Recommendation)
			} else if reqIndex%10 == 0 || reqIndex == totalRequests-1 {
				fmt.Printf("Прогресс: %d/%d (%.1f%%)\n",
					reqIndex+1, totalRequests,
					float64(reqIndex+1)/float64(totalRequests)*100)
			}

			if delay > 0 {
				time.Sleep(time.Duration(delay) * time.Millisecond)
			}
		}(i, req)
	}

	wg.Wait()

	duration := time.Since(startTime)
	rps := float64(totalRequests) / duration.Seconds()

	fmt.Printf("\nТестирование завершено за %.2f сек, %.2f запросов/сек\n",
		duration.Seconds(), rps)

	return stats
}

func sendRequest(req RecommendationRequest, url string, verbose bool) RequestStats {
	stat := RequestStats{}

	jsonData, err := json.Marshal(req)
	if err != nil {
		stat.Error = err
		return stat
	}

	httpReq, err := http.NewRequest("POST", url, bytes.NewBuffer(jsonData))
	if err != nil {
		stat.Error = err
		return stat
	}

	httpReq.Header.Set("Content-Type", "application/json")

	startTime := time.Now()
	client := &http.Client{}
	resp, err := client.Do(httpReq)
	stat.Duration = time.Since(startTime)

	if err != nil {
		stat.Error = err
		return stat
	}
	defer resp.Body.Close()

	stat.StatusCode = resp.StatusCode

	body, err := ioutil.ReadAll(resp.Body)
	if err != nil {
		stat.Error = err
		return stat
	}

	if resp.StatusCode == http.StatusOK {
		var response RecommendationResponse
		err = json.Unmarshal(body, &response)
		if err != nil {
			stat.Error = err
			return stat
		}

		stat.Recommendation = response.Recommendation
		stat.OnPremTotal = response.OnPremTotal
		stat.PrivateTotal = response.PrivateTotal
		stat.PublicTotal = response.PublicTotal
	}

	return stat
}

func analyzeResults(stats []RequestStats, verbose bool) {
	if len(stats) == 0 {
		fmt.Println("Нет данных для анализа")
		return
	}

	successCount := 0
	errorCount := 0
	var totalTime time.Duration
	var minTime time.Duration = time.Hour
	var maxTime time.Duration
	durations := make([]time.Duration, 0, len(stats))

	recommendations := make(map[string]int)

	for _, stat := range stats {
		if stat.Error != nil {
			errorCount++
			if verbose {
				fmt.Printf("Ошибка: %v\n", stat.Error)
			}
			continue
		}

		if stat.StatusCode == http.StatusOK {
			successCount++
			totalTime += stat.Duration
			durations = append(durations, stat.Duration)

			if stat.Duration < minTime {
				minTime = stat.Duration
			}
			if stat.Duration > maxTime {
				maxTime = stat.Duration
			}

			recommendations[stat.Recommendation]++
		} else {
			errorCount++
			if verbose {
				fmt.Printf("Ошибка HTTP: %d\n", stat.StatusCode)
			}
		}
	}

	if len(durations) > 0 {
		for i := 0; i < len(durations); i++ {
			for j := i + 1; j < len(durations); j++ {
				if durations[i] > durations[j] {
					durations[i], durations[j] = durations[j], durations[i]
				}
			}
		}

		avgTime := totalTime / time.Duration(successCount)
		medianIdx := len(durations) / 2
		medianTime := durations[medianIdx]
		p95Idx := int(float64(len(durations)) * 0.95)
		p95Time := durations[p95Idx]

		fmt.Println("\nСтатистика времени ответа:")
		fmt.Printf("  Минимум: %d мс\n", minTime.Milliseconds())
		fmt.Printf("  Среднее: %d мс\n", avgTime.Milliseconds())
		fmt.Printf("  Медиана: %d мс\n", medianTime.Milliseconds())
		fmt.Printf("  95-й перцентиль: %d мс\n", p95Time.Milliseconds())
		fmt.Printf("  Максимум: %d мс\n", maxTime.Milliseconds())
	}

	fmt.Printf("\nУспешных запросов: %d (%.1f%%)\n",
		successCount, float64(successCount)*100/float64(len(stats)))

	if errorCount > 0 {
		fmt.Printf("Ошибок: %d (%.1f%%)\n",
			errorCount, float64(errorCount)*100/float64(len(stats)))
	}

	fmt.Println("\nРаспределение рекомендаций:")
	for rec, count := range recommendations {
		fmt.Printf("  %s: %d (%.1f%%)\n",
			rec, count, float64(count)*100/float64(successCount))
	}
}

func saveResults(stats []RequestStats, filename string) {
	type ResultData struct {
		TotalRequests      int                      `json:"total_requests"`
		SuccessfulRequests int                      `json:"successful_requests"`
		ErrorRequests      int                      `json:"error_requests"`
		MinTimeMs          int64                    `json:"min_time_ms"`
		MaxTimeMs          int64                    `json:"max_time_ms"`
		AvgTimeMs          int64                    `json:"avg_time_ms"`
		MedianTimeMs       int64                    `json:"median_time_ms"`
		P95TimeMs          int64                    `json:"p95_time_ms"`
		Recommendations    map[string]int           `json:"recommendations"`
		DetailedStats      []map[string]interface{} `json:"detailed_stats"`
	}

	result := ResultData{
		TotalRequests:   len(stats),
		Recommendations: make(map[string]int),
		DetailedStats:   make([]map[string]interface{}, 0, len(stats)),
	}

	successCount := 0
	var totalTime time.Duration
	var minTime time.Duration = time.Hour
	var maxTime time.Duration
	durations := make([]time.Duration, 0, len(stats))

	for _, stat := range stats {
		statData := map[string]interface{}{
			"duration_ms": stat.Duration.Milliseconds(),
			"status_code": stat.StatusCode,
		}

		if stat.Error != nil {
			statData["error"] = stat.Error.Error()
		} else if stat.StatusCode == http.StatusOK {
			successCount++
			totalTime += stat.Duration
			durations = append(durations, stat.Duration)

			if stat.Duration < minTime {
				minTime = stat.Duration
			}
			if stat.Duration > maxTime {
				maxTime = stat.Duration
			}

			result.Recommendations[stat.Recommendation]++

			statData["recommendation"] = stat.Recommendation
			statData["on_prem_total"] = stat.OnPremTotal
			statData["private_total"] = stat.PrivateTotal
			statData["public_total"] = stat.PublicTotal
		}

		result.DetailedStats = append(result.DetailedStats, statData)
	}

	result.SuccessfulRequests = successCount
	result.ErrorRequests = len(stats) - successCount

	if len(durations) > 0 {
		for i := 0; i < len(durations); i++ {
			for j := i + 1; j < len(durations); j++ {
				if durations[i] > durations[j] {
					durations[i], durations[j] = durations[j], durations[i]
				}
			}
		}

		result.MinTimeMs = minTime.Milliseconds()
		result.MaxTimeMs = maxTime.Milliseconds()
		result.AvgTimeMs = totalTime.Milliseconds() / int64(successCount)
		result.MedianTimeMs = durations[len(durations)/2].Milliseconds()
		result.P95TimeMs = durations[int(float64(len(durations))*0.95)].Milliseconds()
	}

	jsonData, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		fmt.Printf("Ошибка при создании JSON: %v\n", err)
		return
	}

	err = os.WriteFile(filename, jsonData, 0644)
	if err != nil {
		fmt.Printf("Ошибка при сохранении результатов в файл %s: %v\n", filename, err)
		return
	}

	fmt.Printf("Результаты сохранены в файл %s\n", filename)
}

func contains(arr []string, str string) bool {
	for _, a := range arr {
		if a == str {
			return true
		}
	}
	return false
}
