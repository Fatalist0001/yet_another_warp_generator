// Package services проверяет доступность внешних сервисов через уже поднятый
// SOCKS5-выход. Это отдельная проверка, а не часть пайплайна: она не меняет
// звено и не влияет на туннель.
package services

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Category — группа сервиса в интерфейсе.
type Category string

const (
	// CategoryRU — сервисы, которые ожидаются доступными из России.
	CategoryRU Category = "ru"
	// CategoryBlocked — сервисы, которые обычно недоступны из России.
	CategoryBlocked Category = "blocked"
)

// Service — описание проверяемого сервиса.
type Service struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Category Category `json:"category"`
	URL      string   `json:"url"`
}

// Default — список сервисов для проверки. Меняется только здесь: GUI получает
// его из вывода CLI и не дублирует.
var Default = []Service{
	{ID: "yandex", Label: "Яндекс", Category: CategoryRU, URL: "https://ya.ru/"},
	{ID: "vk", Label: "VK", Category: CategoryRU, URL: "https://vk.com/"},
	{ID: "gosuslugi", Label: "Госуслуги", Category: CategoryRU, URL: "https://www.gosuslugi.ru/"},
	{ID: "ozon", Label: "Ozon", Category: CategoryRU, URL: "https://www.ozon.ru/"},
	{ID: "wildberries", Label: "Wildberries", Category: CategoryRU, URL: "https://www.wildberries.ru/"},
	{ID: "youtube", Label: "YouTube", Category: CategoryBlocked, URL: "https://www.youtube.com/"},
	{ID: "instagram", Label: "Instagram", Category: CategoryBlocked, URL: "https://www.instagram.com/"},
	{ID: "x", Label: "X", Category: CategoryBlocked, URL: "https://x.com/"},
	{ID: "discord", Label: "Discord", Category: CategoryBlocked, URL: "https://discord.com/"},
	{ID: "chatgpt", Label: "ChatGPT", Category: CategoryBlocked, URL: "https://chatgpt.com/"},
	{ID: "telegram", Label: "Telegram", Category: CategoryBlocked, URL: "https://web.telegram.org/"},
	{ID: "gemini", Label: "Gemini", Category: CategoryBlocked, URL: "https://gemini.google.com/"},
}

// Status — результат одной проверки.
type Status string

const (
	StatusOK           Status = "ok"
	StatusSlow         Status = "slow"
	StatusHTTPError    Status = "http_error"
	StatusTimeout      Status = "timeout"
	StatusNetworkError Status = "network_error"
)

// Result — результат проверки сервиса.
type Result struct {
	ID       string   `json:"id"`
	Label    string   `json:"label"`
	Category Category `json:"category"`
	URL      string   `json:"url"`
	Status   Status   `json:"status"`
	Code     int      `json:"code,omitempty"`
	RTTMS    int64    `json:"rtt_ms,omitempty"`
	Error    string   `json:"error,omitempty"`
}

// OK сообщает, что сервис ответил успешно (возможно, медленно).
func (r Result) OK() bool {
	return r.Status == StatusOK || r.Status == StatusSlow
}

// Options — настройки проверки.
type Options struct {
	// Client — HTTP-клиент через SOCKS5. nil — обычный клиент (для тестов).
	Client *http.Client
	// Timeout — таймаут одной проверки.
	Timeout time.Duration
	// Slow — порог «медленного» ответа.
	Slow time.Duration
	// Concurrency — сколько проверок одновременно.
	Concurrency int
}

func (o Options) withDefaults() Options {
	if o.Timeout <= 0 {
		o.Timeout = 8 * time.Second
	}
	if o.Slow <= 0 {
		o.Slow = 2 * time.Second
	}
	if o.Concurrency <= 0 {
		o.Concurrency = 5
	}
	if o.Client == nil {
		o.Client = &http.Client{}
	}
	return o
}

// Check проверяет сервисы параллельно. Порядок результатов совпадает с порядком
// списка, поэтому интерфейс не переставляется между проверками.
func Check(ctx context.Context, list []Service, opts Options) []Result {
	opts = opts.withDefaults()
	results := make([]Result, len(list))
	sem := make(chan struct{}, opts.Concurrency)
	var wg sync.WaitGroup
	for i, svc := range list {
		i, svc := i, svc
		wg.Add(1)
		go func() {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = checkOne(ctx, opts, svc)
		}()
	}
	wg.Wait()
	return results
}

func checkOne(ctx context.Context, opts Options, svc Service) Result {
	res := Result{ID: svc.ID, Label: svc.Label, Category: svc.Category, URL: svc.URL}

	reqCtx, cancel := context.WithTimeout(ctx, opts.Timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, svc.URL, nil)
	if err != nil {
		res.Status = StatusNetworkError
		res.Error = err.Error()
		return res
	}
	req.Header.Set("User-Agent", "warpgen-services/1")
	req.Header.Set("Range", "bytes=0-2048")

	start := time.Now()
	resp, err := opts.Client.Do(req)
	res.RTTMS = time.Since(start).Milliseconds()
	if err != nil {
		res.Status = StatusNetworkError
		if errors.Is(err, context.DeadlineExceeded) || isTimeout(err) {
			res.Status = StatusTimeout
		}
		res.Error = err.Error()
		return res
	}
	defer resp.Body.Close()
	_, _ = io.CopyN(io.Discard, resp.Body, 2048)
	res.Code = resp.StatusCode
	if resp.StatusCode < 200 || resp.StatusCode >= 400 {
		res.Status = StatusHTTPError
		res.Error = strings.TrimSpace(resp.Status)
		return res
	}
	if res.RTTMS > opts.Slow.Milliseconds() {
		res.Status = StatusSlow
		return res
	}
	res.Status = StatusOK
	return res
}

func isTimeout(err error) bool {
	var netErr net.Error
	return errors.As(err, &netErr) && netErr.Timeout()
}
