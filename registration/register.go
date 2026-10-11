package registration

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// regBaseURL — неофициальный API регистрации Cloudflare (как в wgcf/warpscout).
	regBaseURL  = "https://api.cloudflareclient.com/v0a4005/reg"
	apiReachURL = "https://api.cloudflareclient.com/"

	cfClientVersion = "a-6.11-2223"
	cfUserAgent     = "okhttp/3.12.1"

	apiReachTimeout = 3 * time.Second
	registerTimeout = 15 * time.Second
)

// NewClient — HTTP-клиент с таймаутами регистрации.
func NewClient() *http.Client {
	return &http.Client{Timeout: registerTimeout}
}

// APIReachable проверяет, доступен ли API Cloudflare напрямую из текущей сети.
func APIReachable(ctx context.Context, client *http.Client) bool {
	ctx, cancel := context.WithTimeout(ctx, apiReachTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, apiReachURL, nil)
	if err != nil {
		return false
	}
	resp, err := client.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return true
}

type regResp struct {
	ID     string `json:"id"`
	Token  string `json:"token"`
	Config struct {
		Peers []struct {
			PublicKey string `json:"public_key"`
		} `json:"peers"`
		Interface struct {
			Addresses struct {
				V4 string `json:"v4"`
				V6 string `json:"v6"`
			} `json:"addresses"`
		} `json:"interface"`
	} `json:"config"`
}

// ParseRegResponse разбирает ответ API регистрации; privateKey подставляется в
// результат (API приватный ключ не возвращает).
func ParseRegResponse(body []byte, privateKey string) (Account, error) {
	var r regResp
	if err := json.Unmarshal(body, &r); err != nil {
		return Account{}, fmt.Errorf("разбор ответа регистрации: %w", err)
	}
	if len(r.Config.Peers) == 0 || r.Config.Peers[0].PublicKey == "" {
		return Account{}, fmt.Errorf("в ответе регистрации нет публичного ключа пира")
	}
	return Account{
		ID:            r.ID,
		Token:         r.Token,
		PrivateKey:    privateKey,
		PeerPublicKey: r.Config.Peers[0].PublicKey,
		IPv4:          r.Config.Interface.Addresses.V4,
		IPv6:          r.Config.Interface.Addresses.V6,
	}, nil
}

// Register регистрирует новый WARP-аккаунт и включает warp_enabled.
func Register(ctx context.Context, client *http.Client) (Account, error) {
	priv, pub, err := GenerateKeypair()
	if err != nil {
		return Account{}, err
	}

	body, err := doJSON(ctx, client, http.MethodPost, regBaseURL, "", map[string]string{"key": pub})
	if err != nil {
		return Account{}, fmt.Errorf("POST /reg: %w", err)
	}
	a, err := ParseRegResponse(body, priv)
	if err != nil {
		return Account{}, err
	}

	patchURL := fmt.Sprintf("%s/%s", regBaseURL, a.ID)
	if _, err := doJSON(ctx, client, http.MethodPatch, patchURL, a.Token, map[string]bool{"warp_enabled": true}); err != nil {
		return Account{}, fmt.Errorf("PATCH /reg/%s: %w", a.ID, err)
	}
	return a, nil
}

// Rotate меняет ключ существующего аккаунта (PATCH /reg/<id>).
func Rotate(ctx context.Context, client *http.Client, existing Account) (Account, error) {
	priv, pub, err := GenerateKeypair()
	if err != nil {
		return Account{}, err
	}
	body, err := doJSON(ctx, client, http.MethodPatch,
		fmt.Sprintf("%s/%s", regBaseURL, existing.ID), existing.Token,
		map[string]string{"key": pub})
	if err != nil {
		return Account{}, fmt.Errorf("PATCH /reg/%s: %w", existing.ID, err)
	}
	a, err := ParseRegResponse(body, priv)
	if err != nil {
		return Account{}, err
	}
	a.ID, a.Token = existing.ID, existing.Token
	return a, nil
}

// Obtain регистрирует аккаунт: свежий, если existing пуст, иначе ротирует ключ,
// а при неудаче ротации регистрирует новый.
func Obtain(ctx context.Context, client *http.Client, existing Account) (Account, error) {
	if existing.ID == "" || existing.Token == "" {
		return Register(ctx, client)
	}
	a, err := Rotate(ctx, client, existing)
	if err == nil {
		return a, nil
	}
	return Register(ctx, client)
}

func doJSON(ctx context.Context, client *http.Client, method, urlStr, token string, payload any) ([]byte, error) {
	buf, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, method, urlStr, bytes.NewReader(buf))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", cfUserAgent)
	req.Header.Set("CF-Client-Version", cfClientVersion)
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	respBody, err := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("статус %d: %s", resp.StatusCode, strings.TrimSpace(string(respBody)))
	}
	return respBody, nil
}
