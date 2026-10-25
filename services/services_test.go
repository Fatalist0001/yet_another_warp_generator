package services

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCheckClassifiesResponses(t *testing.T) {
	ok := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer ok.Close()
	denied := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer denied.Close()

	list := []Service{
		{ID: "ok", Label: "OK", Category: CategoryRU, URL: ok.URL},
		{ID: "denied", Label: "Denied", Category: CategoryBlocked, URL: denied.URL},
	}
	got := Check(context.Background(), list, Options{Timeout: time.Second, Concurrency: 2})
	if len(got) != 2 {
		t.Fatalf("Check вернул %d результатов, want 2", len(got))
	}
	if got[0].Status != StatusOK || !got[0].OK() {
		t.Errorf("ok: %+v", got[0])
	}
	if got[1].Status != StatusHTTPError || got[1].Code != http.StatusForbidden || got[1].OK() {
		t.Errorf("denied: %+v", got[1])
	}
}

func TestCheckTimeout(t *testing.T) {
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer slow.Close()

	got := Check(context.Background(), []Service{{ID: "slow", URL: slow.URL}}, Options{
		Timeout: 20 * time.Millisecond,
	})
	if len(got) != 1 || got[0].Status != StatusTimeout {
		t.Fatalf("Check = %+v, want timeout", got)
	}
}

func TestDefaultHasRequestedServices(t *testing.T) {
	want := map[string]Category{
		"wildberries": CategoryRU,
		"telegram":    CategoryBlocked,
		"gemini":      CategoryBlocked,
	}
	got := map[string]Category{}
	for _, svc := range Default {
		got[svc.ID] = svc.Category
	}
	for id, category := range want {
		if got[id] != category {
			t.Errorf("сервис %q: категория %q, want %q", id, got[id], category)
		}
	}
	if _, ok := got["avito"]; ok {
		t.Error("Avito должен быть убран из списка")
	}
}
