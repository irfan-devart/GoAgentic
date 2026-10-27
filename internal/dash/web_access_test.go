package dash

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// accessFixture is a fake Access team: a signing key, its key set served
// over TLS, and a token minter.
type accessFixture struct {
	key    *rsa.PrivateKey
	srv    *httptest.Server
	access *Access
	now    time.Time
}

func newAccessFixture(t *testing.T) *accessFixture {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	f := &accessFixture{key: key, now: time.Unix(1_800_000_000, 0)}
	f.srv = httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/cdn-cgi/access/certs" {
			http.NotFound(w, r)
			return
		}
		b64 := base64.RawURLEncoding.EncodeToString
		json.NewEncoder(w).Encode(map[string]any{"keys": []map[string]string{{
			"kid": "k1", "kty": "RSA", "n": b64(key.N.Bytes()), "e": b64(big.NewInt(int64(key.E)).Bytes()),
		}}})
	}))
	t.Cleanup(f.srv.Close)
	f.access = &Access{
		Team: strings.TrimPrefix(f.srv.URL, "https://"), AUD: "aud-1",
		Client: f.srv.Client(), Now: func() time.Time { return f.now },
	}
	return f
}

func (f *accessFixture) token(t *testing.T, claims map[string]any) string {
	t.Helper()
	enc := func(v any) string {
		b, _ := json.Marshal(v)
		return base64.RawURLEncoding.EncodeToString(b)
	}
	base := map[string]any{"aud": []string{"aud-1"}, "iss": f.access.issuer(), "exp": f.now.Add(time.Hour).Unix(), "email": "pat@example.com"}
	for k, v := range claims {
		base[k] = v
	}
	signed := enc(map[string]string{"alg": "RS256", "kid": "k1"}) + "." + enc(base)
	sum := sha256.Sum256([]byte(signed))
	sig, err := rsa.SignPKCS1v15(rand.Reader, f.key, crypto.SHA256, sum[:])
	if err != nil {
		t.Fatal(err)
	}
	return signed + "." + base64.RawURLEncoding.EncodeToString(sig)
}

func TestAccessCheck(t *testing.T) {
	f := newAccessFixture(t)
	req := func(tok string) *http.Request {
		r := httptest.NewRequest("GET", "/", nil)
		if tok != "" {
			r.Header.Set("Cf-Access-Jwt-Assertion", tok)
		}
		return r
	}
	if email, err := f.access.Check(req(f.token(t, nil))); err != nil || email != "pat@example.com" {
		t.Fatalf("valid token: %q, %v", email, err)
	}
	cookie := httptest.NewRequest("GET", "/", nil)
	cookie.AddCookie(&http.Cookie{Name: "CF_Authorization", Value: f.token(t, nil)})
	if _, err := f.access.Check(cookie); err != nil {
		t.Errorf("token in the cookie: %v", err)
	}
	good := f.token(t, nil)
	tampered := good[:strings.LastIndex(good, ".")-2] + "xx" + good[strings.LastIndex(good, "."):]
	for name, tok := range map[string]string{
		"none":         "",
		"garbage":      "a.b.c",
		"tampered":     tampered,
		"wrong aud":    f.token(t, map[string]any{"aud": "other"}),
		"wrong issuer": f.token(t, map[string]any{"iss": "https://evil.cloudflareaccess.com"}),
		"expired":      f.token(t, map[string]any{"exp": f.now.Add(-time.Minute).Unix()}),
		"no expiry":    f.token(t, map[string]any{"exp": 0}),
	} {
		if _, err := f.access.Check(req(tok)); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	f.access.Emails = []string{"Norman@Example.com"}
	if _, err := f.access.Check(req(f.token(t, nil))); err == nil {
		t.Error("email not on the list: accepted")
	}
	if _, err := f.access.Check(req(f.token(t, map[string]any{"email": "norman@example.com"}))); err != nil {
		t.Errorf("listed email: %v", err)
	}
}

func TestWebAccessGuard(t *testing.T) {
	f := newAccessFixture(t)
	h := NewWebHandler(WebSource{Root: t.TempDir(), AllowHosts: []string{"dash.example.com"}, Access: f.access,
		Options: func(bool) Options { return Options{} }})
	get := func(host, tok string) int {
		r := httptest.NewRequest("GET", "/", nil)
		r.Host = host
		if tok != "" {
			r.Header.Set("Cf-Access-Jwt-Assertion", tok)
		}
		w := httptest.NewRecorder()
		h.ServeHTTP(w, r)
		return w.Code
	}
	tok := f.token(t, nil)
	if code := get("dash.example.com", ""); code != http.StatusForbidden {
		t.Errorf("public host, no token = %d, want 403", code)
	}
	if code := get("127.0.0.1:7782", ""); code != http.StatusForbidden {
		t.Errorf("loopback host, no token = %d, want 403 (Access covers every request)", code)
	}
	if code := get("evil.example.com", tok); code != http.StatusForbidden {
		t.Errorf("unknown host with a token = %d, want 403", code)
	}
	if code := get("dash.example.com", tok); code == http.StatusForbidden {
		t.Errorf("public host with a token = %d", code)
	}
}
