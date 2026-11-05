package webserver

import (
	"testing"
	"time"
)

func TestRateLimiterWindow(t *testing.T) {
	now := time.Unix(1700000000, 0)
	l := newRateLimiter(2, time.Minute, func() time.Time { return now })

	if !l.allow("a") || !l.allow("a") {
		t.Fatal("первые два запроса должны пройти")
	}
	if l.allow("a") {
		t.Fatal("третий запрос в окне должен быть отклонён")
	}
	if !l.allow("b") {
		t.Fatal("другой ключ не должен делить лимит")
	}

	now = now.Add(61 * time.Second)
	if !l.allow("a") {
		t.Fatal("после окна запрос должен пройти")
	}
}

func TestRateLimiterDisabled(t *testing.T) {
	for _, limit := range []int{0, -1} {
		l := newRateLimiter(limit, time.Minute, nil)
		for i := 0; i < 100; i++ {
			if !l.allow("a") {
				t.Fatalf("limit=%d не должен ограничивать", limit)
			}
		}
	}
}

func TestETagMatches(t *testing.T) {
	cases := []struct {
		header string
		want   bool
	}{
		{"", false},
		{`"abc"`, true},
		{`W/"abc"`, true},
		{`"x", "abc"`, true},
		{"*", true},
		{`"other"`, false},
	}
	for _, tc := range cases {
		if got := etagMatches(tc.header, `"abc"`); got != tc.want {
			t.Errorf("etagMatches(%q) = %v, want %v", tc.header, got, tc.want)
		}
	}
}
