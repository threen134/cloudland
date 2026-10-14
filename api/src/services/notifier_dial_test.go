package services

import (
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"testing"
)

func TestIsPublicAddr(t *testing.T) {
	cases := map[string]bool{
		"8.8.8.8":          true,
		"52.117.101.145":   true,
		"2606:4700::1111":  true,
		"10.191.202.40":    false,
		"172.18.0.5":       false, // docker bridge, where clapi and alarm-rules-manager live
		"192.168.1.1":      false,
		"127.0.0.1":        false,
		"169.254.169.254":  false,
		"100.64.0.1":       false,
		"0.0.0.0":          false,
		"255.255.255.255":  false,
		"::1":              false,
		"fd00::1":          false,
		"fe80::1":          false,
		"::ffff:10.0.0.1":  false, // IPv4-mapped private
		"64:ff9b::a00:1":   false, // NAT64 of 10.0.0.1
		"64:ff9b:1::a00:1": false, // local-use NAT64 of 10.0.0.1
		"2002:a00:1::1":    false, // 6to4 of 10.0.0.1
		"224.0.0.1":        false,
	}
	for s, want := range cases {
		if got := isPublicAddr(netip.MustParseAddr(s)); got != want {
			t.Errorf("isPublicAddr(%s) = %v, want %v", s, got, want)
		}
	}
}

func TestNotifierClientRefusesInternalTargets(t *testing.T) {
	hit := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit = true }))
	defer srv.Close()

	// Loopback by IP, and by a name that resolves to it
	for _, url := range []string{srv.URL, "http://localhost:" + srv.Listener.Addr().(interface{ String() string }).String()[len("127.0.0.1:"):]} {
		resp, err := newNotifierClient().Get(url)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("request to %s succeeded, want it refused", url)
		}
		if !errors.Is(err, errNonPublicAddr) {
			t.Errorf("request to %s: %v, want errNonPublicAddr", url, err)
		}
	}
	if hit {
		t.Error("the internal server received a request")
	}
}

// With HTTP(S)_PROXY set the proxy (often on an internal address) is dialed, and the target is
// checked before the request is handed to it.
func TestNotifierClientThroughProxy(t *testing.T) {
	var got []string
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got = append(got, r.URL.String())
	}))
	defer proxy.Close()
	proxyURL, _ := url.Parse(proxy.URL)
	client := newNotifierClientWith(http.ProxyURL(proxyURL), net.DefaultResolver)

	resp, err := client.Get("http://93.184.216.34/hook")
	if err != nil {
		t.Fatalf("public target through the proxy: %v", err)
	}
	resp.Body.Close()
	for _, target := range []string{"http://10.0.0.1/hook", "http://[64:ff9b:1::a00:1]/hook", "http://localhost/hook"} {
		resp, err := client.Get(target)
		if err == nil {
			resp.Body.Close()
			t.Fatalf("%s through the proxy succeeded, want it refused", target)
		}
		if !errors.Is(err, errNonPublicAddr) {
			t.Errorf("%s: %v, want errNonPublicAddr", target, err)
		}
	}
	if len(got) != 1 || got[0] != "http://93.184.216.34/hook" {
		t.Fatalf("proxy saw %v, want only the public target", got)
	}
}

func TestNotifierClientRefusesRedirects(t *testing.T) {
	// The redirect check runs before any second connection; drive it directly so the test needs no
	// public server.
	client := newNotifierClient()
	req, _ := http.NewRequest(http.MethodGet, "http://example.com/next", nil)
	if err := client.CheckRedirect(req, []*http.Request{req}); err == nil {
		t.Fatal("redirect allowed, want refused")
	}
}
