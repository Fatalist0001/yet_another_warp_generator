package core

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/chain"
)

// Параметры замера скорости при первичной настройке endpoint'ов.
const (
	// DefaultSpeedURL — небольшой файл Cloudflare. Он отдаётся через WARP и
	// позволяет сравнить endpoint'ы по реальной скорости, а не только по RTT.
	DefaultSpeedURL = "https://speed.cloudflare.com/__down?bytes=524288"
	// DefaultSpeedBytes — 512 КиБ: достаточно, чтобы TLS-хендшейк и TCP slow
	// start не доминировали, но замер не занимал секунды на быстром канале.
	DefaultSpeedBytes int64 = 512 << 10
	// DefaultSpeedTimeout — потолок одного замера.
	DefaultSpeedTimeout = 5 * time.Second
)

// SpeedResult — результат замера скорости.
type SpeedResult struct {
	// Mbps — скорость в мегабитах в секунду.
	Mbps float64
	// Bytes — сколько байт успели прочитать.
	Bytes int64
	// Elapsed — время от начала запроса до конца чтения.
	Elapsed time.Duration
}

// SpeedTestThroughSocks скачивает тестовый файл через SOCKS5. Именно так
// измеряется итоговый выход: в режиме WARP — сам WARP, в режиме с Psiphon —
// вся цепочка.
func SpeedTestThroughSocks(
	ctx context.Context,
	socks string,
	_ string,
	url string,
	maxBytes int64,
	timeout time.Duration,
) (SpeedResult, error) {
	client, err := chain.NewGeoClient(socks)
	if err != nil {
		return SpeedResult{}, fmt.Errorf("speed: SOCKS5-клиент: %w", err)
	}
	if tr, ok := client.Transport.(*http.Transport); ok {
		defer tr.CloseIdleConnections()
	}
	return measureSpeed(ctx, client, url, maxBytes, timeout)
}

// measureSpeed читает не больше maxBytes и считает скорость по фактически
// полученному объёму. Клиент передаётся снаружи: в тестах это локальный
// httptest-сервер, в бою — HTTP-клиент через SOCKS5.
func measureSpeed(
	ctx context.Context,
	client *http.Client,
	url string,
	maxBytes int64,
	timeout time.Duration,
) (SpeedResult, error) {
	if client == nil {
		return SpeedResult{}, errors.New("speed: не задан HTTP-клиент")
	}
	if maxBytes <= 0 {
		maxBytes = DefaultSpeedBytes
	}
	if timeout <= 0 {
		timeout = DefaultSpeedTimeout
	}
	if url == "" {
		url = DefaultSpeedURL
	}

	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, url, nil)
	if err != nil {
		return SpeedResult{}, fmt.Errorf("speed: запрос: %w", err)
	}
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Cache-Control", "no-cache")

	start := time.Now()
	resp, err := client.Do(req)
	if err != nil {
		return SpeedResult{}, fmt.Errorf("speed: запрос: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return SpeedResult{}, fmt.Errorf("speed: статус %d", resp.StatusCode)
	}

	n, err := io.Copy(io.Discard, io.LimitReader(resp.Body, maxBytes))
	elapsed := time.Since(start)
	if err != nil {
		return SpeedResult{}, fmt.Errorf("speed: чтение: %w", err)
	}
	// Сервер может отдать меньше файла. Совсем маленький ответ — это не замер,
	// а ошибка/заглушка: не хотим выбирать endpoint по случайным 2 КБ.
	if n < maxBytes/8 {
		return SpeedResult{}, fmt.Errorf("speed: получено %d из %d байт", n, maxBytes)
	}
	if elapsed <= 0 {
		return SpeedResult{}, errors.New("speed: нулевое время замера")
	}
	return SpeedResult{
		Mbps:    float64(n) * 8 / elapsed.Seconds() / 1_000_000,
		Bytes:   n,
		Elapsed: elapsed,
	}, nil
}
