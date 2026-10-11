package dnscheck

import (
	"context"
	"net"
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
