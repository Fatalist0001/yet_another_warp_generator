package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/scanner"
	"github.com/Fatalist0001/yet_another_warp_generator/tunnel"
)

// runner — автомат состояний пайплайна. Все внешние эффекты идут через зависимости
// Config, всё наблюдаемое — через поток событий.
type runner struct {
	cfg    Config
	clock  Clock
	events chan Event
}

// loop выполняет пайплайн и закрывает поток событий.
func (r *runner) loop(ctx context.Context) {
	defer close(r.events)

	err := r.pipeline(ctx)
	switch {
	case err != nil && ctx.Err() == nil:
		r.emit(Event{Kind: EventStopped, Err: err.Error()})
	default:
		// Остановка по ctx — штатный сценарий, ошибки не несём.
		r.emit(Event{Kind: EventStopped})
	}
}

// pipeline ищет endpoint'ы и крутит попытки поднять цепочку.
//
// Начальная фаза: перебираем кандидатов по порядку, без повторов — если ни один не
// поднялся, пайплайн останавливается с ошибкой. После первого ready включается
// режим удержания: сбой мониторинга уводит к следующей стране, а сломанный внешний
// туннель — к следующему endpoint, и так по кругу до отмены ctx.
func (r *runner) pipeline(ctx context.Context) error {
	cands, err := r.cfg.Locator.Locate(ctx, r.cfg.Endpoints)
	if err != nil {
		return fmt.Errorf("поиск endpoint: %w", err)
	}
	if len(cands) == 0 {
		return errors.New("подходящих endpoint не найдено — проверьте сеть и посторонний VPN")
	}

	cursor := 0
	initialLeft := len(cands)
	holding := false

	for {
		if ctx.Err() != nil {
			return nil
		}
		if holding && !r.sleep(ctx, r.cfg.RetryPause) {
			return nil
		}

		ep := cands[cursor%len(cands)]
		cursor++
		if !holding {
			initialLeft--
		}

		ready, err := r.endpointRound(ctx, ep)
		if err != nil {
			return err
		}
		if ready {
			holding = true
			continue
		}
		if !holding && initialLeft <= 0 {
			return fmt.Errorf("не удалось поднять цепочку ни на одном из %d endpoint'ов", len(cands))
		}
	}
}

// endpointRound пробует один внешний endpoint: туннель → проверка трафика →
// форвардер → перебор стран. Возвращает ready=true, если цепочка хотя бы раз
// дошла до ready (дальше она сломалась или её остановил ctx).
func (r *runner) endpointRound(ctx context.Context, ep Endpoint) (bool, error) {
	r.emit(Event{
		Kind:     EventEndpointSelected,
		Endpoint: ep.Addr,
		Colo:     ep.Colo,
		Region:   ep.Loc,
		RTTMS:    ms(ep.RTT),
	})

	tn, err := r.cfg.Tunnel()
	if err != nil {
		r.failover("не удалось создать туннель: " + err.Error())
		return false, nil
	}
	defer tn.Close()

	if !tn.Handshake(ctx, ep.Addr, r.cfg.HandshakeTimeout) {
		if ctx.Err() != nil {
			return false, nil
		}
		r.failover("нет хендшейка с " + ep.Addr)
		return false, nil
	}
	r.emit(Event{Kind: EventOuterUp, Endpoint: ep.Addr})

	tr, err := tn.FetchTrace(ctx, r.cfg.TraceTimeout)
	if err != nil {
		if ctx.Err() != nil {
			return false, nil
		}
		r.failover("нет данных через туннель: " + err.Error())
		return false, nil
	}
	if reason := traceProblem(tr); reason != "" {
		r.failover(reason)
		return false, nil
	}
	r.emit(Event{Kind: EventOuterChecked, Endpoint: ep.Addr, Colo: tr.Colo, Region: tr.Loc})

	fwd, err := NewForwarder(tn, ForwarderOptions{
		Listen:      r.cfg.Listen,
		Target:      r.cfg.InnerEndpoint,
		MTU:         r.cfg.MTU,
		SessionTTL:  r.cfg.SessionTTL,
		MaxSessions: r.cfg.MaxSessions,
		Logf:        r.cfg.Logf,
	})
	if err != nil {
		r.failover("форвардер: " + err.Error())
		return false, nil
	}
	go func() {
		if err := fwd.Run(ctx); err != nil && ctx.Err() == nil {
			r.log("форвардер остановился: %v", err)
		}
	}()
	defer fwd.Close()
	r.emit(Event{Kind: EventForwarderUp, Forwarder: fwd.Addr()})

	ready := false
	for _, cc := range r.cfg.Countries {
		if ctx.Err() != nil {
			return ready, nil
		}
		sess, err := r.startChain(ctx, fwd.Addr(), cc)
		if err != nil {
			if ctx.Err() != nil {
				return ready, nil
			}
			r.failover(err.Error())
			continue
		}
		ready = true
		r.emit(Event{
			Kind:    EventReady,
			SOCKS:   sess.SOCKS,
			Country: sess.Country,
			IP:      sess.IP,
			Org:     sess.Org,
			Seconds: sess.Elapsed.Seconds(),
		})

		outcome := r.monitor(ctx, tn, sess)
		r.closeSession(sess, cc)
		if outcome != outcomeNextCountry {
			return ready, nil
		}
	}
	return ready, nil
}

// startChain поднимает цепочку на одну страну.
func (r *runner) startChain(ctx context.Context, forwarder, country string) (*ChainSession, error) {
	r.emit(Event{Kind: EventPsiphonStarting, Country: country, Forwarder: forwarder})
	sess, err := r.cfg.Chain.Run(ctx, ChainRequest{Forwarder: forwarder, Countries: []string{country}})
	if err != nil {
		return nil, fmt.Errorf("страна %s: %w", country, err)
	}
	return sess, nil
}

// outcome — чем закончилось наблюдение за живой цепочкой.
type outcome int

const (
	// outcomeStop — контекст отменён.
	outcomeStop outcome = iota
	// outcomeNextCountry — сломалось звено WARP → Psiphon, берём следующую страну.
	outcomeNextCountry
	// outcomeNextEndpoint — сломался внешний туннель, пересоздаём его и форвардер
	// на следующем endpoint.
	outcomeNextEndpoint
)

// monitor следит за живой цепочкой: раз в MonitorInterval проверяет внешний
// туннель (trace изнутри) и выход через SOCKS5. Выход RU меняет звено сразу, прочие
// сбои — после MonitorFailures подряд.
func (r *runner) monitor(ctx context.Context, tn Tunnel, sess *ChainSession) outcome {
	ticker, stop := r.clock.Ticker(r.cfg.MonitorInterval)
	defer stop()

	fails := 0
	for {
		select {
		case <-ctx.Done():
			return outcomeStop
		case <-ticker:
		}
		if ctx.Err() != nil {
			return outcomeStop
		}

		tr, err := tn.FetchTrace(ctx, r.cfg.TraceTimeout)
		if err != nil {
			if ctx.Err() != nil {
				return outcomeStop
			}
			r.failover("внешний туннель перестал отвечать: " + err.Error())
			return outcomeNextEndpoint
		}
		if reason := traceProblem(tr); reason != "" {
			r.failover(reason)
			return outcomeNextEndpoint
		}

		if r.cfg.Prober == nil {
			continue
		}
		res, perr := r.cfg.Prober.Probe(ctx, sess.SOCKS)
		if ctx.Err() != nil {
			return outcomeStop
		}
		if res.RU {
			r.failover(fmt.Sprintf("страна %s: выход через SOCKS5 отдаёт RU", sess.Country))
			return outcomeNextCountry
		}
		if perr != nil || res.Services == 0 {
			fails++
			reason := probeReason(perr)
			r.emit(Event{Kind: EventDegraded, Country: sess.Country, Fails: fails, Reason: reason})
			if fails >= r.cfg.MonitorFailures {
				r.failover(fmt.Sprintf("страна %s: %d сбоя подряд (%s)", sess.Country, fails, reason))
				return outcomeNextCountry
			}
			continue
		}
		fails = 0
	}
}

// probeReason — короткая причина сбоя проверки выхода.
func probeReason(err error) string {
	if err == nil {
		return "ни один геосервис не ответил через SOCKS5"
	}
	return firstLine(err.Error())
}

// traceProblem — что не так с trace изнутри туннеля ("" — всё в порядке).
func traceProblem(tr tunnel.TraceResult) string {
	if !strings.Contains(tr.Raw, "warp=on") {
		return "в трафике через туннель нет warp=on — это не WARP-endpoint"
	}
	colo := strings.ToUpper(strings.TrimSpace(tr.Colo))
	if scanner.ExcludedColos[colo] {
		return "туннель приземлился на исключённый узел " + colo + " (DME/LED/KJA)"
	}
	return ""
}

// closeSession завершает дочерние процессы цепочки.
func (r *runner) closeSession(sess *ChainSession, country string) {
	if sess == nil || sess.Close == nil {
		return
	}
	if err := sess.Close(); err != nil {
		r.log("страна %s: закрытие цепочки: %v", country, err)
	}
}

// emit отправляет событие, не блокируя пайплайн на медленном читателе.
func (r *runner) emit(ev Event) {
	ev.Time = r.clock.Now()
	select {
	case r.events <- ev:
	case <-time.After(emitTimeout):
	}
}

// failover сообщает о смене звена.
func (r *runner) failover(reason string) {
	r.emit(Event{Kind: EventFailover, Reason: reason})
}

// sleep ждёт заданное время, прерываясь по отмене ctx (false — контекст отменён).
func (r *runner) sleep(ctx context.Context, d time.Duration) bool {
	if d <= 0 {
		return ctx.Err() == nil
	}
	select {
	case <-ctx.Done():
		return false
	case <-r.clock.After(d):
		return true
	}
}

func (r *runner) log(format string, args ...any) {
	if r.cfg.Logf != nil {
		r.cfg.Logf(format, args...)
	}
}

// ms — задержка в миллисекундах (0 — неизвестно).
func ms(d time.Duration) int64 {
	if d <= 0 {
		return 0
	}
	return d.Milliseconds()
}

// firstLine оставляет первую строку сообщения (ошибки могут быть многострочными).
func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return strings.TrimSpace(s)
}
