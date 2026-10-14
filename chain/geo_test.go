package chain

import (
	"bufio"
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
)

// geoServer — тестовый геосервис с заданным телом и кодом ответа.
func geoServer(t *testing.T, body string, status int) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = io.WriteString(w, body)
	}))
	t.Cleanup(srv.Close)
	return srv
}

type svcResp struct {
	body   string
	status int
}

// servicesFrom поднимает по тестовому сервису на каждый ответ.
func servicesFrom(t *testing.T, resps ...svcResp) []geoService {
	t.Helper()
	out := make([]geoService, len(resps))
	for i, r := range resps {
		srv := geoServer(t, r.body, r.status)
		out[i] = geoService{Name: "svc" + strconv.Itoa(i+1), URL: srv.URL}
	}
	return out
}

const nlBody = `{"ip":"203.0.113.9","country":"NL","org":"AS123 Test"}`

func TestEvaluateSuccess(t *testing.T) {
	svcs := servicesFrom(t, svcResp{nlBody, 200})
	results, err := evaluate(context.Background(), http.DefaultClient, svcs, "NL", true)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(results) != 1 || results[0].Country != "NL" || results[0].IP != "203.0.113.9" {
		t.Errorf("results = %+v", results)
	}
}

// Ответ-челлендж (HTML вместо JSON) не считается готовностью, но не мешает
// второму сервису подтвердить страну.
func TestEvaluateHTMLChallenge(t *testing.T) {
	svcs := servicesFrom(t,
		svcResp{"<html><title>Just a moment...</title></html>", 200},
		svcResp{nlBody, 200},
	)
	results, err := evaluate(context.Background(), http.DefaultClient, svcs, "NL", true)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(results) != 1 || results[0].Country != "NL" {
		t.Errorf("ожидали только ответ второго сервиса, получили %+v", results)
	}
}

func TestEvaluateRateLimitedSkipped(t *testing.T) {
	svcs := servicesFrom(t,
		svcResp{"rate limited", 429},
		svcResp{nlBody, 200},
	)
	results, err := evaluate(context.Background(), http.DefaultClient, svcs, "NL", true)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(results) != 1 {
		t.Errorf("429 должен пропускаться, получили %+v", results)
	}
}

func TestEvaluateAllSkipped(t *testing.T) {
	svcs := servicesFrom(t,
		svcResp{"rate limited", 429},
		svcResp{"<html>challenge</html>", 200},
	)
	if _, err := evaluate(context.Background(), http.DefaultClient, svcs, "NL", true); err != errNoGeoService {
		t.Fatalf("ожидали errNoGeoService, получили %v", err)
	}
}

func TestEvaluateRU(t *testing.T) {
	svcs := servicesFrom(t, svcResp{`{"ip":"95.1.2.3","country":"RU"}`, 200})
	if _, err := evaluate(context.Background(), http.DefaultClient, svcs, "NL", true); err != errExitRU {
		t.Fatalf("ожидали errExitRU, получили %v", err)
	}
}

func TestEvaluateStrictMismatch(t *testing.T) {
	svcs := servicesFrom(t, svcResp{`{"ip":"203.0.113.5","country":"US"}`, 200})
	if _, err := evaluate(context.Background(), http.DefaultClient, svcs, "NL", true); err != errWrongCountry {
		t.Fatalf("ожидали errWrongCountry, получили %v", err)
	}
}

func TestEvaluateNonStrictAcceptsOtherCountry(t *testing.T) {
	svcs := servicesFrom(t, svcResp{`{"ip":"203.0.113.5","country":"US"}`, 200})
	if _, err := evaluate(context.Background(), http.DefaultClient, svcs, "NL", false); err != nil {
		t.Fatalf("без strict достаточно «не RU», получили %v", err)
	}
}

// Расхождение сервисов (как на замерах с PL): один назвал запрошенную страну,
// второй — другую. Успех по строгому критерию, но оба ответа возвращаются.
func TestEvaluateServiceDisagreement(t *testing.T) {
	svcs := servicesFrom(t,
		svcResp{`{"ip":"57.128.192.119","country":"PL","org":"AS16276 OVH"}`, 200},
		svcResp{`{"ip":"57.128.192.119","country":"FR"}`, 200},
	)
	results, err := evaluate(context.Background(), http.DefaultClient, svcs, "PL", true)
	if err != nil {
		t.Fatalf("evaluate: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("ожидали два ответа, получили %+v", results)
	}
}

func TestQueryGeoRejectsNonIPv4(t *testing.T) {
	svcs := servicesFrom(t, svcResp{`{"ip":"2001:db8::1","country":"NL"}`, 200})
	if _, err := queryGeo(context.Background(), http.DefaultClient, svcs[0]); err == nil {
		t.Error("IPv6-адрес не должен считаться корректным IPv4")
	}
}

// newGeoClient должен реально ходить через SOCKS5 и резолвить имя на стороне
// прокси (аналог socks5h) — проверяем на поддельном SOCKS5-сервере.
func TestGeoClientThroughSOCKS5(t *testing.T) {
	srv := geoServer(t, `{"ip":"198.51.100.7","country":"SE","org":"AS999 Test"}`, 200)

	socksAddr := fakeSOCKS(t, srv.Listener.Addr().String())
	client, err := newGeoClient(socksAddr)
	if err != nil {
		t.Fatalf("newGeoClient: %v", err)
	}
	res, err := queryGeo(context.Background(), client, geoService{Name: "socks", URL: "http://geotest.invalid/json"})
	if err != nil {
		t.Fatalf("queryGeo через SOCKS5: %v", err)
	}
	if res.Country != "SE" || res.IP != "198.51.100.7" {
		t.Errorf("res = %+v", res)
	}
}

// fakeSOCKS — минимальный SOCKS5-сервер без аутентификации: на любой CONNECT
// подключается к заданному backend, имитируя удалённое разрешение имени.
func fakeSOCKS(t *testing.T, backend string) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("слушаем SOCKS5: %v", err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go serveSOCKSConn(conn, backend)
		}
	}()
	return ln.Addr().String()
}

func serveSOCKSConn(conn net.Conn, backend string) {
	defer conn.Close()
	br := bufio.NewReader(conn)

	// Приветствие: VER NMETHODS METHODS...
	ver, err := br.ReadByte()
	if err != nil || ver != 0x05 {
		return
	}
	nMethods, err := br.ReadByte()
	if err != nil {
		return
	}
	if _, err := br.Discard(int(nMethods)); err != nil {
		return
	}
	if _, err := conn.Write([]byte{0x05, 0x00}); err != nil {
		return
	}

	// Запрос: VER CMD RSV ATYP ADDR PORT
	req := make([]byte, 4)
	if _, err := io.ReadFull(br, req); err != nil {
		return
	}
	if req[0] != 0x05 || req[1] != 0x01 {
		return
	}
	switch req[3] {
	case 0x01: // IPv4
		if _, err := br.Discard(4); err != nil {
			return
		}
	case 0x03: // домен
		l, err := br.ReadByte()
		if err != nil {
			return
		}
		if _, err := br.Discard(int(l)); err != nil {
			return
		}
	case 0x04: // IPv6
		if _, err := br.Discard(16); err != nil {
			return
		}
	default:
		return
	}
	if _, err := br.Discard(2); err != nil {
		return
	}

	// Ответ: успех, BND.ADDR = 0.0.0.0:0.
	reply := []byte{0x05, 0x00, 0x00, 0x01, 0, 0, 0, 0, 0, 0}
	if _, err := conn.Write(reply); err != nil {
		return
	}

	up, err := net.Dial("tcp", backend)
	if err != nil {
		return
	}
	defer up.Close()

	done := make(chan struct{}, 2)
	go func() { _, _ = io.Copy(up, br); done <- struct{}{} }()
	go func() { _, _ = io.Copy(conn, up); done <- struct{}{} }()
	<-done
}
