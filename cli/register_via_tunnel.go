package cli

import (
	"context"
	"errors"
	"fmt"
	"math/rand"
	"net"
	"net/http"
	"os"
	"time"

	"github.com/Fatalist0001/yet_another_warp_generator/config"
	"github.com/Fatalist0001/yet_another_warp_generator/registration"
	"github.com/Fatalist0001/yet_another_warp_generator/scanner"
	"github.com/Fatalist0001/yet_another_warp_generator/tunnel"
)

// Имя хоста API Cloudflare, к которому ходим через туннель.
const regAPIHost = "api.cloudflareclient.com:443"

const (
	tunnelRegSample = 64
	tunnelRegBudget = 40 * time.Second
	tunnelRegClient = 15 * time.Second
)

// registerViaTunnel регистрирует свежий аккаунт, отправляя запросы к API сквозь
// userspace-туннель, поднятый на ключах существующего аккаунта. Нужен, когда API
// Cloudflare недоступен напрямую.
func registerViaTunnel(ctx context.Context, existing registration.Account, iface string, debug bool) (registration.Account, error) {
	tn, err := tunnel.New(tunnelOptions(existing, config.NewConfig(), iface))
	if err != nil {
		return registration.Account{}, err
	}
	defer tn.Close()

	ctx, cancel := context.WithTimeout(ctx, tunnelRegBudget)
	defer cancel()

	cands := scanner.Expand(scanner.DefaultPrefixes(), scanner.DefaultPorts)
	sampled := sampleCandidates(cands, tunnelRegSample)

	for _, c := range sampled {
		if ctx.Err() != nil {
			break
		}
		ep := c.Endpoint()
		if !tn.Handshake(ctx, ep, scanner.DefaultHandshakeTimeout) {
			continue
		}
		if debug {
			fmt.Fprintf(os.Stderr, "  хендшейк прошёл на %s\n", ep)
		}
		client := tunnelHTTPClient(tn)
		a, err := registration.Obtain(ctx, client, registration.Account{})
		if err != nil {
			continue
		}
		return a, nil
	}
	return registration.Account{}, errors.New("не нашли endpoint, через который можно зарегистрироваться")
}

// tunnelHTTPClient — HTTP-клиент, чей транспорт ходит через туннель.
func tunnelHTTPClient(tn *tunnel.Tunnel) *http.Client {
	return &http.Client{
		Timeout: tunnelRegClient,
		Transport: &http.Transport{
			DisableKeepAlives: true,
			DialContext: func(ctx context.Context, network, _ string) (net.Conn, error) {
				return tn.DialContext(ctx, network, regAPIHost)
			},
		},
	}
}

func sampleCandidates(cands []scanner.Candidate, n int) []scanner.Candidate {
	if n <= 0 || n >= len(cands) {
		return cands
	}
	out := make([]scanner.Candidate, n)
	for i, j := range rand.Perm(len(cands))[:n] {
		out[i] = cands[j]
	}
	return out
}
