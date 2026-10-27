package dash

import (
	"crypto"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

// Access checks Cloudflare Access's signed token on every request, so the
// dashboard can sit behind a Cloudflare Tunnel: the tunnel only carries the
// request, the token proves Access let this person in. With Access set, a
// request without a valid token is refused, whatever its Host.
type Access struct {
	// Team is the Access team domain, "<team>.cloudflareaccess.com".
	Team string
	// AUD is the Access application's audience tag.
	AUD string
	// Emails, when set, are the only identities let through, on top of the
	// Access policy.
	Emails []string
	// Client fetches the team's signing keys; nil means a 10 s client.
	Client *http.Client
	// Now is the clock; nil means time.Now.
	Now func() time.Time

	mu      sync.Mutex
	keys    map[string]*rsa.PublicKey
	fetched time.Time
}

// accessKeysMaxAge is how long fetched keys are trusted before a refetch;
// an unknown key id refetches sooner, at most once a minute.
const accessKeysMaxAge = time.Hour

func (a *Access) now() time.Time {
	if a.Now != nil {
		return a.Now()
	}
	return time.Now()
}

func (a *Access) issuer() string { return "https://" + strings.TrimSuffix(a.Team, "/") }

// Check returns the token's email if r carries a valid Access token.
func (a *Access) Check(r *http.Request) (string, error) {
	tok := r.Header.Get("Cf-Access-Jwt-Assertion")
	if tok == "" {
		if c, err := r.Cookie("CF_Authorization"); err == nil {
			tok = c.Value
		}
	}
	if tok == "" {
		return "", errors.New("no Access token")
	}
	parts := strings.Split(tok, ".")
	if len(parts) != 3 {
		return "", errors.New("malformed token")
	}
	var head struct{ Alg, Kid string }
	if err := decodeSegment(parts[0], &head); err != nil {
		return "", err
	}
	if head.Alg != "RS256" {
		return "", fmt.Errorf("unexpected alg %q", head.Alg)
	}
	key, err := a.key(head.Kid)
	if err != nil {
		return "", err
	}
	sig, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil {
		return "", errors.New("malformed signature")
	}
	sum := sha256.Sum256([]byte(parts[0] + "." + parts[1]))
	if err := rsa.VerifyPKCS1v15(key, crypto.SHA256, sum[:], sig); err != nil {
		return "", errors.New("bad signature")
	}
	var claims struct {
		Aud   audience `json:"aud"`
		Iss   string   `json:"iss"`
		Exp   int64    `json:"exp"`
		Nbf   int64    `json:"nbf"`
		Email string   `json:"email"`
	}
	if err := decodeSegment(parts[1], &claims); err != nil {
		return "", err
	}
	now := a.now().Unix()
	switch {
	case !claims.Aud.has(a.AUD):
		return "", errors.New("wrong audience")
	case claims.Iss != a.issuer():
		return "", errors.New("wrong issuer")
	case claims.Exp == 0 || now >= claims.Exp:
		return "", errors.New("token expired")
	case claims.Nbf != 0 && now < claims.Nbf-60:
		return "", errors.New("token not yet valid")
	}
	if len(a.Emails) > 0 && !containsFold(a.Emails, claims.Email) {
		return "", fmt.Errorf("%s is not allowed", claims.Email)
	}
	return claims.Email, nil
}

// audience is the aud claim, a string or a list of them.
type audience []string

func (au *audience) UnmarshalJSON(b []byte) error {
	var one string
	if json.Unmarshal(b, &one) == nil {
		*au = audience{one}
		return nil
	}
	var many []string
	if err := json.Unmarshal(b, &many); err != nil {
		return err
	}
	*au = many
	return nil
}

func (au audience) has(s string) bool {
	for _, x := range au {
		if s != "" && x == s {
			return true
		}
	}
	return false
}

func containsFold(list []string, s string) bool {
	for _, x := range list {
		if s != "" && strings.EqualFold(strings.TrimSpace(x), s) {
			return true
		}
	}
	return false
}

func decodeSegment(seg string, v any) error {
	b, err := base64.RawURLEncoding.DecodeString(seg)
	if err != nil {
		return errors.New("malformed token")
	}
	if err := json.Unmarshal(b, v); err != nil {
		return errors.New("malformed token")
	}
	return nil
}

// key returns the team's signing key with this id, fetching the key set
// when it is stale or the id is unknown.
func (a *Access) key(kid string) (*rsa.PublicKey, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	age := a.now().Sub(a.fetched)
	if k := a.keys[kid]; k != nil && age < accessKeysMaxAge {
		return k, nil
	}
	if a.keys == nil || age >= time.Minute {
		keys, err := a.fetchKeys()
		if err != nil {
			if k := a.keys[kid]; k != nil {
				return k, nil // keep serving on a failed refresh
			}
			return nil, err
		}
		a.keys, a.fetched = keys, a.now()
	}
	if k := a.keys[kid]; k != nil {
		return k, nil
	}
	return nil, errors.New("unknown signing key")
}

func (a *Access) fetchKeys() (map[string]*rsa.PublicKey, error) {
	client := a.Client
	if client == nil {
		client = &http.Client{Timeout: 10 * time.Second}
	}
	resp, err := client.Get(a.issuer() + "/cdn-cgi/access/certs")
	if err != nil {
		return nil, fmt.Errorf("fetching Access keys: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching Access keys: %s", resp.Status)
	}
	var set struct {
		Keys []struct{ Kid, Kty, N, E string }
	}
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("reading Access keys: %w", err)
	}
	keys := map[string]*rsa.PublicKey{}
	for _, k := range set.Keys {
		if k.Kty != "RSA" {
			continue
		}
		n, err1 := base64.RawURLEncoding.DecodeString(k.N)
		e, err2 := base64.RawURLEncoding.DecodeString(k.E)
		if err1 != nil || err2 != nil || len(e) == 0 || len(e) > 4 {
			continue
		}
		keys[k.Kid] = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(new(big.Int).SetBytes(e).Int64())}
	}
	if len(keys) == 0 {
		return nil, errors.New("no RSA keys in the Access key set")
	}
	return keys, nil
}
