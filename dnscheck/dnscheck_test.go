package dnscheck

import (
	"context"
	"encoding/binary"
	"io"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/net/dns/dnsmessage"
)

func dnsResponse(id uint16, withAnswer bool) []byte {
	msg := dnsmessage.Message{
		Header: dnsmessage.Header{ID: id, Response: true, RecursionAvailable: true},
		Questions: []dnsmessage.Question{{
			Name:  dnsmessage.MustNewName(QueryName),
			Type:  dnsmessage.TypeA,
			Class: dnsmessage.ClassINET,
		}},
	}
	if withAnswer {
		msg.Answers = []dnsmessage.Resource{{
			Header: dnsmessage.ResourceHeader{
				Name:  dnsmessage.MustNewName(QueryName),
				Type:  dnsmessage.TypeA,
				Class: dnsmessage.ClassINET,
				TTL:   60,
			},
			Body: &dnsmessage.AResource{A: [4]byte{1, 1, 1, 1}},
		}}
	}
	packed, err := msg.Pack()
	if err != nil {
		panic(err)
	}
	return packed
}

// pipeDialer имитирует DNS-сервер: handler получает запрос, возвращает ответ
// (nil означает «нет ответа» — соединение остаётся открытым до таймаута).
type pipeDialer struct {
	handler func(query []byte) []byte
}

func (p pipeDialer) DialContext(_ context.Context, _, _ string) (net.Conn, error) {
	c1, c2 := net.Pipe()
	go func() {
		buf := make([]byte, 512)
		n, err := c2.Read(buf)
		if err != nil {
			c2.Close()
			return
		}
		resp := p.handler(buf[:n])
		if resp == nil {
			return
		}
		c2.Write(resp)
		c2.Close()
	}()
	return c1, nil
}

// tcpDNSDialer — поддельный DNS-over-TCP: отвечает на фрейм «длина + сообщение»,
// умеет задерживать ответ и молчать (проверка выбора резолвера и таймаутов).
type tcpDNSDialer struct {
	delays map[string]time.Duration
	silent map[string]bool

	mu    sync.Mutex
	addrs []string
}

func (d *tcpDNSDialer) DialContext(_ context.Context, _, address string) (net.Conn, error) {
	d.mu.Lock()
	d.addrs = append(d.addrs, address)
	delay, silent := d.delays[address], d.silent[address]
	d.mu.Unlock()

	client, server := net.Pipe()
	go func() {
		defer server.Close()
		var head [2]byte
		if _, err := io.ReadFull(server, head[:]); err != nil {
			return
		}
		query := make([]byte, int(binary.BigEndian.Uint16(head[:])))
		if _, err := io.ReadFull(server, query); err != nil {
			return
		}
		if silent {
			_, _ = io.Copy(io.Discard, server)
			return
		}
		if delay > 0 {
			time.Sleep(delay)
		}
		var m dnsmessage.Message
		if err := m.Unpack(query); err != nil {
			return
		}
		resp := dnsResponse(m.Header.ID, true)
		frame := make([]byte, 2+len(resp))
		binary.BigEndian.PutUint16(frame, uint16(len(resp)))
		copy(frame[2:], resp)
		_, _ = server.Write(frame)
	}()
	return client, nil
}

func (d *tcpDNSDialer) called() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.addrs...)
}

func tcpTargets() []Target {
	return []Target{
		{Name: "1.1.1.1:53", IP: "1.1.1.1", Port: 53},
		{Name: "8.8.8.8:53", IP: "8.8.8.8", Port: 53},
	}
}

func TestCheckTCPOK(t *testing.T) {
	d := &tcpDNSDialer{}
	r := CheckTCP(context.Background(), d, Target{Name: "1.1.1.1", IP: "1.1.1.1"}, Options{Timeout: time.Second})
	if r.Status != StatusOK {
		t.Fatalf("статус = %v (err=%v), want OK", r.Status, r.Err)
	}
	if r.RTT <= 0 {
		t.Errorf("RTT = %v, want > 0", r.RTT)
	}
	if got := d.called(); len(got) != 1 || got[0] != "1.1.1.1:53" {
		t.Errorf("дозвон по %v, ожидали 1.1.1.1:53", got)
	}
}

func TestCheckTCPCustomPort(t *testing.T) {
	d := &tcpDNSDialer{}
	r := CheckTCP(context.Background(), d, Target{Name: "dns", IP: "9.9.9.9", Port: 5353}, Options{Timeout: time.Second})
	if r.Status != StatusOK {
		t.Fatalf("статус = %v (err=%v)", r.Status, r.Err)
	}
	if got := d.called(); len(got) != 1 || got[0] != "9.9.9.9:5353" {
		t.Errorf("дозвон по %v, ожидали 9.9.9.9:5353", got)
	}
	if r.IP != "9.9.9.9:5353" {
		t.Errorf("IP = %q", r.IP)
	}
}

func TestCheckTCPNoAnswer(t *testing.T) {
	d := &tcpDNSDialer{silent: map[string]bool{"8.8.8.8:53": true}}
	r := CheckTCP(context.Background(), d, Target{Name: "8.8.8.8", IP: "8.8.8.8"}, Options{Timeout: 100 * time.Millisecond})
	if r.Status != StatusNoAnswer {
		t.Fatalf("статус = %v (err=%v), want NoAnswer", r.Status, r.Err)
	}
}

func TestCheckTCPInvalidIP(t *testing.T) {
	d := &tcpDNSDialer{}
	r := CheckTCP(context.Background(), d, Target{Name: "bad", IP: "not-an-ip"}, Options{Timeout: time.Second})
	if r.Status != StatusError {
		t.Fatalf("статус = %v, want Error", r.Status)
	}
	if len(d.called()) != 0 {
		t.Error("на некорректный IP дозвон не делаем")
	}
}

func TestSelectBestTCP(t *testing.T) {
	d := &tcpDNSDialer{delays: map[string]time.Duration{"1.1.1.1:53": 40 * time.Millisecond}}
	best, results, err := SelectBestTCP(context.Background(), d, tcpTargets(), Options{Timeout: time.Second})
	if err != nil {
		t.Fatalf("SelectBestTCP: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("результатов %d, ожидали 2", len(results))
	}
	if best.IP != "8.8.8.8" {
		t.Errorf("лучший = %q, ожидали 8.8.8.8", best.IP)
	}
}

func TestSelectBestTCPTakesTheOnlyWorking(t *testing.T) {
	d := &tcpDNSDialer{silent: map[string]bool{"1.1.1.1:53": true}}
	best, results, err := SelectBestTCP(context.Background(), d, tcpTargets(), Options{Timeout: 100 * time.Millisecond})
	if err != nil {
		t.Fatalf("SelectBestTCP: %v", err)
	}
	if best.IP != "8.8.8.8" {
		t.Errorf("лучший = %q, ожидали 8.8.8.8", best.IP)
	}
	if len(results) != 2 || results[0].Status.OK() {
		t.Errorf("результаты = %+v", results)
	}
}

func TestSelectBestTCPNoneAnswers(t *testing.T) {
	d := &tcpDNSDialer{silent: map[string]bool{"1.1.1.1:53": true, "8.8.8.8:53": true}}
	best, results, err := SelectBestTCP(context.Background(), d, tcpTargets(), Options{Timeout: 100 * time.Millisecond})
	if err == nil {
		t.Fatalf("ожидали ошибку, получили лучший резолвер %q", best.IP)
	}
	if !strings.Contains(err.Error(), "ни один DNS") {
		t.Errorf("текст ошибки = %q", err)
	}
	if len(results) != 2 {
		t.Errorf("результатов %d, ожидали 2 — нужны для журнала", len(results))
	}
}

func TestExchangeTCPRejectsEmpty(t *testing.T) {
	c1, c2 := net.Pipe()
	defer c1.Close()
	defer c2.Close()
	if _, err := ExchangeTCP(c1, nil, time.Second); err == nil {
		t.Error("пустой запрос должен быть отвергнут")
	}
}

func TestBuildAndParseQuery(t *testing.T) {
	query, id, err := buildQuery(QueryName)
	if err != nil {
		t.Fatalf("buildQuery: %v", err)
	}
	var msg dnsmessage.Message
	if err := msg.Unpack(query); err != nil {
		t.Fatalf("собранный запрос не разбирается: %v", err)
	}
	if msg.Header.ID != id {
		t.Errorf("id запроса = %d, want %d", msg.Header.ID, id)
	}
	if len(msg.Questions) != 1 || msg.Questions[0].Type != dnsmessage.TypeA {
		t.Errorf("ожидался один A-вопрос: %+v", msg.Questions)
	}
}

func TestParseResponseAnswers(t *testing.T) {
	id := uint16(4242)
	n, err := parseResponse(dnsResponse(id, true), id)
	if err != nil {
		t.Fatalf("parseResponse: %v", err)
	}
	if n != 1 {
		t.Errorf("answers = %d, want 1", n)
	}

	if n, err := parseResponse(dnsResponse(id, false), id); err != nil || n != 0 {
		t.Errorf("ответ без адресов: n=%d err=%v", n, err)
	}
}

func TestParseResponseWrongID(t *testing.T) {
	if _, err := parseResponse(dnsResponse(1, true), 2); err == nil {
		t.Error("ожидалась ошибка при несовпадении id")
	}
}

func TestCheckOK(t *testing.T) {
	d := pipeDialer{handler: func(q []byte) []byte {
		var m dnsmessage.Message
		_ = m.Unpack(q)
		return dnsResponse(m.Header.ID, true)
	}}
	r := Check(context.Background(), d, Target{Name: "1.1.1.1", IP: "1.1.1.1"}, Options{Timeout: time.Second})
	if r.Status != StatusOK {
		t.Fatalf("статус = %v (err=%v), want OK", r.Status, r.Err)
	}
	if r.RTT <= 0 {
		t.Errorf("RTT = %v, want > 0", r.RTT)
	}
}

func TestCheckSlow(t *testing.T) {
	d := pipeDialer{handler: func(q []byte) []byte {
		time.Sleep(50 * time.Millisecond)
		var m dnsmessage.Message
		_ = m.Unpack(q)
		return dnsResponse(m.Header.ID, true)
	}}
	r := Check(context.Background(), d, Target{Name: "1.1.1.1", IP: "1.1.1.1"}, Options{Timeout: time.Second, Slow: 10 * time.Millisecond})
	if r.Status != StatusSlow {
		t.Fatalf("статус = %v, want Slow", r.Status)
	}
	if !r.Status.OK() {
		t.Error("медленный DNS должен считаться пригодным")
	}
}

func TestCheckNoAnswer(t *testing.T) {
	d := pipeDialer{handler: func([]byte) []byte { return nil }}
	r := Check(context.Background(), d, Target{Name: "8.8.8.8", IP: "8.8.8.8"}, Options{Timeout: 100 * time.Millisecond})
	if r.Status != StatusNoAnswer {
		t.Fatalf("статус = %v (err=%v), want NoAnswer", r.Status, r.Err)
	}
}

func TestCheckInvalidIP(t *testing.T) {
	d := pipeDialer{handler: func([]byte) []byte { return nil }}
	r := Check(context.Background(), d, Target{Name: "bad", IP: "not-an-ip"}, Options{Timeout: time.Second})
	if r.Status != StatusError {
		t.Fatalf("статус = %v, want Error", r.Status)
	}
}

func TestCheckAll(t *testing.T) {
	d := pipeDialer{handler: func(q []byte) []byte {
		var m dnsmessage.Message
		_ = m.Unpack(q)
		return dnsResponse(m.Header.ID, true)
	}}
	targets := []Target{{Name: "1.1.1.1", IP: "1.1.1.1"}, {Name: "8.8.8.8", IP: "8.8.8.8"}}
	results := CheckAll(context.Background(), d, targets, Options{Timeout: time.Second})
	if len(results) != 2 {
		t.Fatalf("CheckAll вернул %d, want 2", len(results))
	}
	for _, r := range results {
		if r.Status != StatusOK {
			t.Errorf("%s: статус %v", r.Name, r.Status)
		}
	}
}

func TestStatusStringAndOK(t *testing.T) {
	for _, s := range []Status{StatusOK, StatusSlow, StatusNoAnswer, StatusError} {
		if s.String() == "" {
			t.Errorf("статус %d без строки", s)
		}
	}
	if !StatusOK.OK() || !StatusSlow.OK() {
		t.Error("OK и Slow должны быть пригодны")
	}
	if StatusNoAnswer.OK() || StatusError.OK() {
		t.Error("NoAnswer и Error не должны быть пригодны")
	}
}

func TestPresetTargets(t *testing.T) {
	targets, _ := PresetTargets()
	if len(targets) == 0 {
		t.Error("PresetTargets вернул пустой список")
	}
}

func TestFormatTable(t *testing.T) {
	out := FormatTable([]Result{{Name: "1.1.1.1", IP: "1.1.1.1", Status: StatusOK, RTT: 20 * time.Millisecond}})
	if out == "" {
		t.Fatal("пустая таблица")
	}
}
