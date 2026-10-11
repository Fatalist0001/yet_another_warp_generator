package tunnel

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// TraceURL — endpoint, через который проверяем, что видит внешний мир через
// туннель. Поля `loc` (регион, как видит Cloudflare) и `colo` (узел) описаны
// в инструкции.
const TraceURL = "https://www.cloudflare.com/cdn-cgi/trace"

// TraceResult — разобранный ответ cdn-cgi/trace.
type TraceResult struct {
	// Loc — регион, который видит внешний сервис (SEEN AS).
	Loc string
	// Colo — узел Cloudflare, на который попал туннель (NODE).
	Colo string
	// IP — внешний адрес через туннель.
	IP string
	// Raw — сырой ответ.
	Raw string
}

// FetchTrace делает HTTP-запрос через туннель и разбирает cdn-cgi/trace.
func (t *Tunnel) FetchTrace(ctx context.Context, timeout time.Duration) (TraceResult, error) {
	if timeout <= 0 {
		timeout = DefaultTimeout
	}
	reqCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, TraceURL, nil)
	if err != nil {
		return TraceResult{}, err
	}
	t.client.Timeout = timeout
	resp, err := t.client.Do(req)
	if err != nil {
		return TraceResult{}, fmt.Errorf("trace: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return TraceResult{}, fmt.Errorf("trace: чтение: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return TraceResult{}, fmt.Errorf("trace: статус %d", resp.StatusCode)
	}
	return ParseTrace(string(body)), nil
}

// ParseTrace разбирает ответ cdn-cgi/trace (строки key=value).
func ParseTrace(body string) TraceResult {
	tr := TraceResult{Raw: body}
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		key, val, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		switch strings.TrimSpace(key) {
		case "loc":
			tr.Loc = strings.TrimSpace(val)
		case "colo":
			tr.Colo = strings.TrimSpace(val)
		case "ip":
			tr.IP = strings.TrimSpace(val)
		}
	}
	return tr
}
