package orchestrator

import (
	"reflect"
	"testing"

	"github.com/llanc/vantu/internal/store"
)

func TestNormalizeProxyOptions(t *testing.T) {
	got, err := normalizeProxyOptions(store.ProxyOptions{
		UpstreamHost:    "  app.lan:8080 ",
		IPAllowlist:     []string{" 10.1.2.3/8", "203.0.113.7", "", "10.0.0.0/8", "2001:db8::1"},
		RequestHeaders:  map[string]string{"x-env": " prod "},
		ResponseHeaders: map[string]string{"cache-control": "no-store"},
		Compress:        true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := store.ProxyOptions{
		UpstreamHost:    "app.lan:8080",
		IPAllowlist:     []string{"10.0.0.0/8", "203.0.113.7", "2001:db8::1"},
		RequestHeaders:  map[string]string{"X-Env": "prod"},
		ResponseHeaders: map[string]string{"Cache-Control": "no-store"},
		Compress:        true,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %+v\nwant %+v", got, want)
	}

	bad := []store.ProxyOptions{
		{UpstreamHost: "http://app.lan"},
		{UpstreamHost: "app lan"},
		{IPAllowlist: []string{"10.0.0.300"}},
		{RequestHeaders: map[string]string{"Bad Header": "x"}},
		{RequestHeaders: map[string]string{"host": "x"}},
		{RequestHeaders: map[string]string{"X-Vantu-Relay-Key": "x"}},
		{ResponseHeaders: map[string]string{"Content-Length": "1"}},
		{ResponseHeaders: map[string]string{"X-A": "a\r\nInjected: 1"}},
	}
	for _, p := range bad {
		if _, err := normalizeProxyOptions(p); err == nil {
			t.Errorf("expected validation error for %+v", p)
		}
	}
}
