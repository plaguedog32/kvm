package localonly

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
)

func TestLocalDestinations(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "10.0.0.2", "172.16.1.2", "192.168.1.2", "::1", "fd00::1", "fe80::1%eth0", "jetkvm", "machine.local"} {
		if !LocalHost(host) {
			t.Error("rejected local host", host)
		}
	}
	for _, host := range []string{"1.1.1.1", "8.8.8.8", "172.15.1.2", "192.168.1.2.evil.test", "api.jetkvm.com", "accounts.google.com", "[::1]", "10.0.0.1;bad", ""} {
		if LocalHost(host) {
			t.Error("accepted external/malformed host", host)
		}
	}
}
func TestExternalHTTPAndRedirectsAreBlocked(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("local")) }))
	defer target.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "https://api.jetkvm.com/releases", http.StatusFound)
	}))
	defer redirect.Close()
	client := &http.Client{Transport: ConfigureTransport(http.DefaultTransport.(*http.Transport).Clone())}
	r, err := client.Get(target.URL)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	if _, err = client.Get(redirect.URL); err == nil {
		t.Fatal("redirect escaped local network")
	}
	for _, raw := range []string{"https://api.jetkvm.com", "http://1.1.1.1", "http://127.0.0.1@evil.test"} {
		u, _ := url.Parse(raw)
		if CheckURL(u) == nil {
			t.Error("accepted external URL", raw)
		}
	}
	if _, err := DialContext(context.Background(), "tcp", "api.jetkvm.com:443"); err == nil {
		t.Fatal("public host was dialed")
	}
}
