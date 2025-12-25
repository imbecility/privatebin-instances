package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/PuerkitoBio/goquery"
	"github.com/imbecility/go-fake-useragent/useragent"
)

const (
	directoryURL = "https://privatebin.info/directory/"
	outputFile   = "privatebin_instances.json"
	concurrency  = 50               // количество одновременных потоков
	goodUptime   = 99.0             // порог хорошего аптайма
	siteTimeout  = 15 * time.Second // таймаут на проверку одного сайта
)

// suspiciousPhrases хранит фразы из alert-элементов, которые указывают на ненадежность инстанса
var suspiciousPhrases = []string{
	"test service",
	"deleted anytime",
	"long-term storage",
	"testing purposes",
}

// Instance структура одной записи
type Instance struct {
	Version string  `json:"version"`
	Address string  `json:"address"`
	Uptime  float64 `json:"uptime"`
}

// FinalResult результирующий JSON
type FinalResult struct {
	Reliable   []Instance `json:"reliable"`   // uptime >= 99% и без угроз внезапно удалить данные
	LowUptime  []Instance `json:"low_uptime"` // uptime < 99% и без угроз внезапно удалить данные
	Unreliable []Instance `json:"unreliable"` // не надежные, данные могут быть удалены в любое время
}

// checkResult - промежуточная структура для передачи результатов из воркера
type checkResult struct {
	instance Instance
	category string // "reliable", "low_uptime", "unreliable", "discard"
	err      error
}

// initInfrastructure создает HTTP клиент с браузерными заголовками
func initInfrastructure() (*http.Client, *useragent.Generator, error) {
	uaGen, err := useragent.NewGenerator(useragent.WithDiskCache(os.TempDir(), 24*time.Hour))
	if err != nil {
		return nil, nil, fmt.Errorf("ошибка UA: %w", err)
	}

	client := &http.Client{
		Timeout: siteTimeout,
		Transport: &http.Transport{
			MaxIdleConns:        100,
			MaxIdleConnsPerHost: 10,
			IdleConnTimeout:     90 * time.Second,
		},
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			return nil
		},
	}

	return client, uaGen, nil
}

// fetchPage загружает страницу
func fetchPage(client *http.Client, uaGen *useragent.Generator, url string) (*goquery.Document, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}

	headers := uaGen.GetHeaders(url)
	for key, value := range headers {
		req.Header.Set(key, value)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func(Body io.ReadCloser) {
		cberr := Body.Close()
		if cberr != nil {
			log.Printf("не удалось закрыть тело страницы '%s': %v", url, cberr)
		}
	}(resp.Body)

	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("status %d", resp.StatusCode)
	}

	return goquery.NewDocumentFromReader(resp.Body)
}

// parseDirectory собирает все инстансы с валидным числовым аптаймом
func parseDirectory(doc *goquery.Document) []Instance {
	var instances []Instance

	doc.Find("h5").Each(func(i int, s *goquery.Selection) {
		versionRaw := strings.TrimSpace(s.Text())
		version := strings.TrimPrefix(versionRaw, "Version ")

		table := s.Next()
		if !table.Is("table") {
			return
		}

		table.Find("tbody tr").Each(func(j int, tr *goquery.Selection) {
			tds := tr.Find("td")
			if tds.Length() < 8 {
				return
			}
			address := strings.TrimSpace(tds.Eq(0).Text())
			uptimeRaw := strings.TrimSpace(tds.Eq(7).Text())
			uptimeClean := strings.ReplaceAll(uptimeRaw, "%", "")

			val, err := strconv.ParseFloat(strings.TrimSpace(uptimeClean), 64)

			if err == nil {
				instances = append(instances, Instance{
					Version: version,
					Address: address,
					Uptime:  val,
				})
			}
		})
	})
	return instances
}

// processConcurrently запускает воркеры и собирает результаты и ошибки
func processConcurrently(client *http.Client, uaGen *useragent.Generator, list []Instance) (FinalResult, map[string]error) {
	jobs := make(chan Instance, len(list))
	results := make(chan checkResult, len(list))
	var wg sync.WaitGroup

	for w := 0; w < concurrency; w++ {
		wg.Add(1)
		go worker(client, uaGen, &wg, jobs, results)
	}

	for _, instance := range list {
		jobs <- instance
	}
	close(jobs)

	go func() {
		wg.Wait()
		close(results)
	}()

	var final FinalResult
	errorsMap := make(map[string]error)

	count := 0
	total := len(list)

	for res := range results {
		count++
		if count%10 == 0 || count == total {
			fmt.Printf("\rОбработано: %d/%d", count, total)
		}

		if res.err != nil {
			errorsMap[res.instance.Address] = res.err
			continue
		}

		switch res.category {
		case "reliable":
			final.Reliable = append(final.Reliable, res.instance)
		case "low_uptime":
			final.LowUptime = append(final.LowUptime, res.instance)
		case "unreliable":
			final.Unreliable = append(final.Unreliable, res.instance)
		case "discard":
		}
	}
	fmt.Println()
	return final, errorsMap
}

// worker функция, которая выполняется в горутине
func worker(client *http.Client, uaGen *useragent.Generator, wg *sync.WaitGroup, jobs <-chan Instance, results chan<- checkResult) {
	defer wg.Done()

	for inst := range jobs {
		res := checkInstance(client, uaGen, inst)
		results <- res
	}
}

// checkInstance проверяет один конкретный сайт
func checkInstance(client *http.Client, uaGen *useragent.Generator, inst Instance) checkResult {
	doc, err := fetchPage(client, uaGen, inst.Address)
	if err != nil {
		return checkResult{instance: inst, category: "discard", err: err}
	}

	// поддерживает ли вечное хранение
	hasNever := false
	doc.Find("select#pasteExpiration option").Each(func(i int, s *goquery.Selection) {
		if val, exists := s.Attr("value"); exists && val == "never" {
			hasNever = true
		}
	})

	if !hasNever {
		return checkResult{instance: inst, category: "discard", err: nil}
	}

	// проверка на угрозы удаления контента
	isSuspicious := false
	doc.Find("div.alert.alert-info[role='alert']").Each(func(i int, s *goquery.Selection) {
		text := strings.ToLower(s.Text())
		for _, phrase := range suspiciousPhrases {
			if strings.Contains(text, phrase) {
				isSuspicious = true
			}
		}
	})

	if isSuspicious {
		return checkResult{instance: inst, category: "unreliable", err: nil}
	}

	if inst.Uptime < goodUptime {
		return checkResult{instance: inst, category: "low_uptime", err: nil}
	}

	return checkResult{instance: inst, category: "reliable", err: nil}
}

func saveJSON(filename string, data interface{}) error {
	file, err := os.Create(filename)
	if err != nil {
		return err
	}
	defer func(file *os.File) {
		fcerr := file.Close()
		if fcerr != nil {
			log.Printf("ОШИБКА: не удалось корректно закрыть файл с результатами: %v", fcerr)
		}
	}(file)

	encoder := json.NewEncoder(file)
	encoder.SetIndent("", "  ")
	return encoder.Encode(data)
}

func main() {
	start := time.Now()

	client, uaGen, err := initInfrastructure()
	if err != nil {
		log.Fatalf("ошибка инициализации: %v", err)
	}

	log.Printf("#1 сбор списка всех инстансов")
	doc, err := fetchPage(client, uaGen, directoryURL)
	if err != nil {
		log.Fatalf("ошибка загрузки каталога: %v", err)
	}

	allInstances := parseDirectory(doc)
	log.Printf("всего найдено кандидатов в таблице: %d", len(allInstances))

	log.Printf("#2: параллельная проверка по %v инстансов", concurrency)
	finalData, errs := processConcurrently(client, uaGen, allInstances)

	log.Printf("проверка завершена за %v", time.Since(start))
	log.Printf("итоги: reliable: %d | low_uptime: %d | unreliable: %d", len(finalData.Reliable), len(finalData.LowUptime), len(finalData.Unreliable))

	if len(errs) > 0 {
		log.Printf("ошибок при доступе: %d", len(errs))
		for addr, err := range errs {
			log.Printf("ОШИБКА [%s]: %v", addr, err)
		}
	}

	if sverr := saveJSON(outputFile, finalData); sverr != nil {
		log.Fatalf("ошибка сохранения файла: %v", sverr)
	}
	log.Printf("результат сохранен в %s", outputFile)
}
