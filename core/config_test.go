package core

import (
	"testing"
	"time"
)

// Дефолты мониторинга должны быть инерционными: одиночная просадка сети или один
// неудачный ответ геосервиса не должны рвать работающую цепочку (с --tun это
// выглядит как обрыв интернета у пользователя).
func TestConfigMonitoringDefaults(t *testing.T) {
	got := Config{}.withDefaults()

	if got.MonitorInterval != DefaultMonitorInterval {
		t.Errorf("MonitorInterval = %v, ожидали %v", got.MonitorInterval, DefaultMonitorInterval)
	}
	if got.MonitorFailures != DefaultMonitorFailures {
		t.Errorf("MonitorFailures = %d, ожидали %d", got.MonitorFailures, DefaultMonitorFailures)
	}
	if got.TraceFailures != DefaultTraceFailures {
		t.Errorf("TraceFailures = %d, ожидали %d", got.TraceFailures, DefaultTraceFailures)
	}
	if got.RUFailures != DefaultRUFailures {
		t.Errorf("RUFailures = %d, ожидали %d", got.RUFailures, DefaultRUFailures)
	}
	if got.RetryPause != DefaultRetryPause {
		t.Errorf("RetryPause = %v, ожидали %v", got.RetryPause, DefaultRetryPause)
	}
	if got.TraceTimeout != DefaultTraceTimeout {
		t.Errorf("TraceTimeout = %v, ожидали %v", got.TraceTimeout, DefaultTraceTimeout)
	}

	// Ни один порог не должен срабатывать с первой неудачной проверки.
	if DefaultTraceFailures < 2 || DefaultRUFailures < 2 || DefaultMonitorFailures < 2 {
		t.Errorf("пороги должны требовать серию сбоев: trace=%d, RU=%d, probe=%d",
			DefaultTraceFailures, DefaultRUFailures, DefaultMonitorFailures)
	}
	if DefaultRetryPause < 10*time.Second {
		t.Errorf("пауза перед новой попыткой %v слишком мала: переключения будут частыми", DefaultRetryPause)
	}
}

// Явно заданные значения не перетираются дефолтами.
func TestConfigExplicitValuesWin(t *testing.T) {
	custom := Config{
		MonitorInterval: time.Minute,
		MonitorFailures: 1,
		TraceFailures:   2,
		RUFailures:      2,
		RetryPause:      time.Second,
		TraceTimeout:    time.Second,
	}.withDefaults()

	if custom.MonitorInterval != time.Minute {
		t.Errorf("MonitorInterval = %v, ожидали минуту", custom.MonitorInterval)
	}
	if custom.MonitorFailures != 1 || custom.TraceFailures != 2 || custom.RUFailures != 2 {
		t.Errorf("пороги перетёрты дефолтами: probe=%d, trace=%d, RU=%d",
			custom.MonitorFailures, custom.TraceFailures, custom.RUFailures)
	}
	if custom.RetryPause != time.Second || custom.TraceTimeout != time.Second {
		t.Errorf("тайминги перетёрты дефолтами: пауза %v, trace %v", custom.RetryPause, custom.TraceTimeout)
	}
}
