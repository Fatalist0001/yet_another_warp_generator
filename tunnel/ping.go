package tunnel

import (
	"net/netip"
	"time"

	"golang.org/x/net/icmp"
	"golang.org/x/net/ipv4"
)

// Константы замера задержки внутри туннеля (не ICMP-пинг хоста).
const (
	PingInterval  = 200 * time.Millisecond
	DefaultPings  = 10
	tornTailFails = 3
	pingID        = 0xbeef
)

// PingResult — результат замера через туннель.
type PingResult struct {
	// RTT — средняя задержка по ответившим эхо.
	RTT time.Duration
	// Loss — доля потерянных эхо (0..1).
	Loss float32
	// Torn — туннель проходит данные, но затем рвётся и не восстанавливается
	// (признак среза ТСПУ).
	Torn bool
}

// Ping измеряет задержку и стабильность внутри туннеля по адресу dst.
func (t *Tunnel) Ping(dst netip.Addr, count int, timeout time.Duration) PingResult {
	if count <= 0 {
		count = DefaultPings
	}
	if timeout <= 0 {
		timeout = 2 * time.Second
	}

	pc, err := t.tnet.DialPingAddr(netip.Addr{}, dst)
	if err != nil {
		return PingResult{Loss: 1}
	}
	defer pc.Close()

	sent := make([]time.Time, count)
	got := make([]bool, count)
	buf := make([]byte, 1500)
	n := 0
	var total time.Duration

	drain := func(deadline time.Time) {
		for n < count {
			pc.SetReadDeadline(deadline)
			m, err := pc.Read(buf)
			if err != nil {
				return
			}
			seq, ok := parseEchoSeq(buf[:m])
			if !ok || seq < 0 || seq >= count || got[seq] || sent[seq].IsZero() {
				continue
			}
			got[seq] = true
			total += time.Since(sent[seq])
			n++
		}
	}

	for seq := 0; seq < count; seq++ {
		if sendEcho(pc, seq) {
			sent[seq] = time.Now()
		}
		drain(time.Now().Add(PingInterval))
	}
	drain(time.Now().Add(timeout))

	return PingResult{
		RTT:  pingSummary(total, n),
		Loss: lossFraction(n, count),
		Torn: teardown(got),
	}
}

func sendEcho(pc interface {
	Write([]byte) (int, error)
}, seq int) bool {
	msg := icmp.Message{
		Type: ipv4.ICMPTypeEcho,
		Body: &icmp.Echo{ID: pingID, Seq: seq, Data: []byte("warpgen")},
	}
	wire, err := msg.Marshal(nil)
	if err != nil {
		return false
	}
	_, err = pc.Write(wire)
	return err == nil
}

func parseEchoSeq(b []byte) (int, bool) {
	m, err := icmp.ParseMessage(ipv4.ICMPTypeEchoReply.Protocol(), b)
	if err != nil {
		return 0, false
	}
	echo, ok := m.Body.(*icmp.Echo)
	if !ok || m.Type != ipv4.ICMPTypeEchoReply {
		return 0, false
	}
	return echo.Seq, true
}

func pingSummary(total time.Duration, got int) time.Duration {
	if got == 0 {
		return 0
	}
	return total / time.Duration(got)
}

func lossFraction(got, count int) float32 {
	if count == 0 {
		return 0
	}
	return float32(count-got) / float32(count)
}

// teardown — хвост из не менее чем tornTailFails подряд пропущенных эхо.
func teardown(results []bool) bool {
	if len(results) < tornTailFails {
		return false
	}
	for _, ok := range results[len(results)-tornTailFails:] {
		if ok {
			return false
		}
	}
	return true
}

// Durability — двойной замер: реальный срез ТСПУ остаётся мёртвым в обеих
// сериях, транзиентная потеря — нет.
func (t *Tunnel) Durability(dst netip.Addr, count int, timeout time.Duration) PingResult {
	first := t.Ping(dst, count, timeout)
	if !first.Torn {
		return first
	}
	second := t.Ping(dst, count, timeout)
	if !second.Torn {
		return second
	}
	return first
}
