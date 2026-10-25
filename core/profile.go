package core

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Режимы профиля: скорость WARP и итоговая скорость WARP → Psiphon различаются,
// поэтому кэш хранит их отдельно.
const (
	ProfileWarpOnly = "warp_only"
	ProfilePsiphon  = "psiphon"
)

// ProfileEndpoint — endpoint, сохранённый после первичного замера.
type ProfileEndpoint struct {
	Addr   string  `json:"addr"`
	Colo   string  `json:"colo,omitempty"`
	Region string  `json:"region,omitempty"`
	RTTMS  int64   `json:"rtt_ms,omitempty"`
	Mbps   float64 `json:"mbps,omitempty"`
}

// EndpointProfile — выбранная конфигурация для одного режима.
type EndpointProfile struct {
	Mode      string            `json:"mode"`
	Countries []string          `json:"countries,omitempty"`
	Endpoints []ProfileEndpoint `json:"endpoints"`
	UpdatedAt time.Time         `json:"updated_at"`
}

// ProfileFile — содержимое файла кэша.
type ProfileFile struct {
	Version  int               `json:"version"`
	Profiles []EndpointProfile `json:"profiles"`
}

// LoadProfile читает сохранённые endpoint'ы для режима и списка стран.
//
// ok=false означает, что подходящего профиля нет: нужен первичный скан и замер.
func LoadProfile(path, mode string, countries []string) ([]Endpoint, bool, error) {
	if strings.TrimSpace(path) == "" {
		return nil, false, nil
	}
	file, err := readProfileFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, false, nil
		}
		return nil, false, err
	}
	for _, p := range file.Profiles {
		if p.Mode != mode || !sameCountries(p.Countries, countries) {
			continue
		}
		out := make([]Endpoint, 0, len(p.Endpoints))
		for _, ep := range p.Endpoints {
			if strings.TrimSpace(ep.Addr) == "" {
				continue
			}
			out = append(out, Endpoint{
				Addr:      ep.Addr,
				Colo:      ep.Colo,
				Loc:       ep.Region,
				RTT:       time.Duration(ep.RTTMS) * time.Millisecond,
				SpeedMbps: ep.Mbps,
			})
		}
		if len(out) == 0 {
			return nil, false, nil
		}
		return out, true, nil
	}
	return nil, false, nil
}

// SaveProfile сохраняет профиль, не теряя записи другого режима.
func SaveProfile(path, mode string, countries []string, endpoints []Endpoint) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	file, err := readProfileFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		// Битый файл не должен мешать подключению: заменяем его новым.
		file = ProfileFile{}
	}
	file.Version = 1

	profile := EndpointProfile{
		Mode:      mode,
		Countries: append([]string(nil), countries...),
		UpdatedAt: time.Now().UTC(),
		Endpoints: make([]ProfileEndpoint, 0, len(endpoints)),
	}
	for _, ep := range endpoints {
		if strings.TrimSpace(ep.Addr) == "" {
			continue
		}
		profile.Endpoints = append(profile.Endpoints, ProfileEndpoint{
			Addr:   ep.Addr,
			Colo:   ep.Colo,
			Region: ep.Loc,
			RTTMS:  ep.RTT.Milliseconds(),
			Mbps:   ep.SpeedMbps,
		})
	}
	if len(profile.Endpoints) == 0 {
		return errors.New("profile: нечего сохранять")
	}

	replaced := false
	for i := range file.Profiles {
		if file.Profiles[i].Mode == mode && sameCountries(file.Profiles[i].Countries, countries) {
			file.Profiles[i] = profile
			replaced = true
			break
		}
	}
	if !replaced {
		file.Profiles = append(file.Profiles, profile)
	}

	data, err := json.MarshalIndent(file, "", "  ")
	if err != nil {
		return fmt.Errorf("profile: JSON: %w", err)
	}
	data = append(data, '\n')
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("profile: каталог %s: %w", dir, err)
	}
	tmp := path + ".tmp"
	// В профиле нет ключей — только адреса, задержки и скорости. Права 0644
	// нужны, чтобы профиль, записанный root-процессом (`--tun`), прочитал
	// пользователь при следующем обычном запуске.
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("profile: запись %s: %w", tmp, err)
	}
	// WriteFile не меняет права уже существующего .tmp (например, после
	// прошлой записи под root с umask 077): выставляем явно.
	if err := os.Chmod(tmp, 0o644); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("profile: права %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("profile: замена %s: %w", path, err)
	}
	return nil
}

// RemoveProfile удаляет кэш: следующий запуск снова выберет endpoint'ы по
// скорости. Отсутствующий файл — не ошибка.
func RemoveProfile(path string) error {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("profile: удаление %s: %w", path, err)
	}
	return nil
}

func readProfileFile(path string) (ProfileFile, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return ProfileFile{}, err
	}
	var file ProfileFile
	if err := json.Unmarshal(data, &file); err != nil {
		return ProfileFile{}, fmt.Errorf("profile: разбор %s: %w", path, err)
	}
	return file, nil
}

func sameCountries(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if !strings.EqualFold(strings.TrimSpace(a[i]), strings.TrimSpace(b[i])) {
			return false
		}
	}
	return true
}
