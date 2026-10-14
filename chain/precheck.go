package chain

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/scanner"
	"github.com/Fatalist0001/yet_another_warp_generator/tunnel"
)

// outerTraceTimeout — потолок на проверку внешнего туннеля.
const outerTraceTimeout = 15 * time.Second

// checkOuterTunnel убеждается, что трафик по системному маршруту уже идёт через
// WARP (внешний AWG-туннель поднят) и узел не из исключённого списка
// (DME/LED/KJA — там блокировки по SNI+IP). Список берём из scanner.ExcludedColos,
// чтобы не дублировать его второй раз.
func checkOuterTunnel(ctx context.Context, client *http.Client, traceURL string) (tunnel.TraceResult, error) {
	reqCtx, cancel := context.WithTimeout(ctx, outerTraceTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, traceURL, nil)
	if err != nil {
		return tunnel.TraceResult{}, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return tunnel.TraceResult{}, fmt.Errorf("внешний туннель не проверяется: нет доступа к %s (%w)", traceURL, err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if err != nil {
		return tunnel.TraceResult{}, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return tunnel.TraceResult{}, fmt.Errorf("trace вернул статус %d", resp.StatusCode)
	}

	tr := tunnel.ParseTrace(string(body))
	if !strings.Contains(tr.Raw, "warp=on") {
		return tr, fmt.Errorf("внешний туннель не поднят: в trace нет warp=on — сначала подключите AWG-конфиг (AmneziaVPN), затем повторите")
	}
	if scanner.ExcludedColos[strings.ToUpper(strings.TrimSpace(tr.Colo))] {
		return tr, fmt.Errorf("внешний туннель приземлился на исключённый узел %s (DME/LED/KJA) — выберите другой endpoint и перегенерируйте конфиг", tr.Colo)
	}
	return tr, nil
}
