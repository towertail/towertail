package api

import (
	"net/http/httptest"
	"testing"
)

func TestIPExtractor(t *testing.T) {
	for _, tc := range []struct {
		name, header, remote string
		set                  map[string]string
		want                 string
	}{
		{"direct ignores XFF", "", "203.0.113.5:1234", map[string]string{"X-Forwarded-For": "1.2.3.4"}, "203.0.113.5"},
		{"XFF from private proxy", "X-Forwarded-For", "172.18.0.3:1234", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "198.51.100.7"},
		{"XFF from public peer", "X-Forwarded-For", "203.0.113.5:1234", map[string]string{"X-Forwarded-For": "198.51.100.7"}, "203.0.113.5"},
		{"CF header", "CF-Connecting-IP", "172.18.0.4:1234", map[string]string{"CF-Connecting-IP": "198.51.100.8"}, "198.51.100.8"},
		{"CF header missing", "CF-Connecting-IP", "172.18.0.4:1234", nil, "172.18.0.4"},
		{"CF header not an IP", "CF-Connecting-IP", "172.18.0.4:1234", map[string]string{"CF-Connecting-IP": "x"}, "172.18.0.4"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := httptest.NewRequest("GET", "/", nil)
			r.RemoteAddr = tc.remote
			for k, v := range tc.set {
				r.Header.Set(k, v)
			}
			if got := ipExtractor(tc.header)(r); got != tc.want {
				t.Fatalf("ip = %s, want %s", got, tc.want)
			}
		})
	}
}
