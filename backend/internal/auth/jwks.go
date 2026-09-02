package auth

import (
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"sync"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// jwksHTTPClient is used for all JWKS fetches. Unlike http.DefaultClient it has
// a timeout, so a hung identity provider can't pin a request goroutine.
var jwksHTTPClient = &http.Client{Timeout: 10 * time.Second}

// jwksTTL is how long a fetched key set is trusted before a refresh. A key
// rotation is still picked up sooner via the on-miss refetch in verifyJWTWithJWKS.
const jwksTTL = 10 * time.Minute

type cachedKeySet struct {
	keys    map[string]*rsa.PublicKey // kid -> public key ("" kid allowed for single-key sets)
	fetched time.Time
}

var (
	jwksMu    sync.Mutex
	jwksStore = map[string]cachedKeySet{}
)

// getJWKSKey returns the RSA public key for the given kid from the JWKS at url,
// using a short-lived cache. If the kid isn't cached (or forceRefresh is set) it
// refetches once — this both bootstraps the cache and picks up key rotations.
func getJWKSKey(url, kid string, forceRefresh bool) (*rsa.PublicKey, error) {
	jwksMu.Lock()
	entry, ok := jwksStore[url]
	fresh := ok && time.Since(entry.fetched) < jwksTTL
	jwksMu.Unlock()

	if fresh && !forceRefresh {
		if key, found := lookupKey(entry.keys, kid); found {
			return key, nil
		}
		// Fall through to a refetch on a cache miss (possible rotation).
	}

	keys, err := fetchJWKS(url)
	if err != nil {
		// If we have a usable (if stale) cached set, fall back to it rather
		// than failing logins on a transient JWKS outage.
		if ok {
			if key, found := lookupKey(entry.keys, kid); found {
				return key, nil
			}
		}
		return nil, err
	}

	jwksMu.Lock()
	jwksStore[url] = cachedKeySet{keys: keys, fetched: time.Now()}
	jwksMu.Unlock()

	if key, found := lookupKey(keys, kid); found {
		return key, nil
	}
	return nil, fmt.Errorf("no matching key for kid %q", kid)
}

func lookupKey(keys map[string]*rsa.PublicKey, kid string) (*rsa.PublicKey, bool) {
	if kid != "" {
		if key, ok := keys[kid]; ok {
			return key, true
		}
	}
	// If the token carried no kid (or none matched) and the set has exactly one
	// key, use it — common for single-signing-key providers.
	if len(keys) == 1 {
		for _, key := range keys {
			return key, true
		}
	}
	return nil, false
}

// jwkKey mirrors one entry of a JWKS document.
type jwkKey struct {
	Kty string   `json:"kty"`
	Kid string   `json:"kid"`
	N   string   `json:"n"`
	E   string   `json:"e"`
	X5c []string `json:"x5c"`
}

type jwkSet struct {
	Keys []jwkKey `json:"keys"`
}

func fetchJWKS(url string) (map[string]*rsa.PublicKey, error) {
	resp, err := jwksHTTPClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch JWKS: status %s", resp.Status)
	}

	var set jwkSet
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("failed to decode JWKS: %w", err)
	}

	keys := make(map[string]*rsa.PublicKey)
	for _, k := range set.Keys {
		if k.Kty != "" && k.Kty != "RSA" {
			continue
		}
		pub, err := jwkToRSAPublicKey(k)
		if err != nil {
			continue // skip unusable keys rather than failing the whole set
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("JWKS contained no usable RSA keys")
	}
	return keys, nil
}

func jwkToRSAPublicKey(k jwkKey) (*rsa.PublicKey, error) {
	// Prefer the explicit modulus/exponent form.
	if k.N != "" && k.E != "" {
		nBytes, err := base64.RawURLEncoding.DecodeString(k.N)
		if err != nil {
			return nil, err
		}
		eBytes, err := base64.RawURLEncoding.DecodeString(k.E)
		if err != nil {
			return nil, err
		}
		n := new(big.Int).SetBytes(nBytes)
		var e int
		if len(eBytes) > 4 {
			return nil, fmt.Errorf("exponent too large")
		}
		padded := make([]byte, 4)
		copy(padded[4-len(eBytes):], eBytes)
		e = int(binary.BigEndian.Uint32(padded))
		if e == 0 {
			return nil, fmt.Errorf("invalid exponent")
		}
		return &rsa.PublicKey{N: n, E: e}, nil
	}

	// Fall back to an x5c certificate chain.
	if len(k.X5c) > 0 {
		der, err := base64.StdEncoding.DecodeString(k.X5c[0])
		if err != nil {
			return nil, err
		}
		cert, err := x509.ParseCertificate(der)
		if err != nil {
			// Try a PEM-wrapped form as a last resort.
			block, _ := pem.Decode([]byte(k.X5c[0]))
			if block == nil {
				return nil, err
			}
			cert, err = x509.ParseCertificate(block.Bytes)
			if err != nil {
				return nil, err
			}
		}
		if pub, ok := cert.PublicKey.(*rsa.PublicKey); ok {
			return pub, nil
		}
		return nil, fmt.Errorf("x5c certificate is not RSA")
	}

	return nil, fmt.Errorf("unsupported JWK")
}

// verifyJWTWithJWKS parses and cryptographically verifies tokenString against the
// JWKS at jwksURL, populating claims. It pins the signing algorithm to the RSA
// family (so alg:none and HMAC confusion are rejected) and applies any extra
// parser options (audience, issuer, expiration-required). On a kid cache miss it
// refetches the JWKS once before giving up.
func verifyJWTWithJWKS(tokenString, jwksURL string, claims jwt.Claims, opts ...jwt.ParserOption) error {
	if jwksURL == "" {
		return fmt.Errorf("no JWKS URL configured; cannot verify token signature")
	}

	attempted := false
	keyFunc := func(token *jwt.Token) (interface{}, error) {
		if _, ok := token.Method.(*jwt.SigningMethodRSA); !ok {
			return nil, fmt.Errorf("unexpected signing method: %v", token.Header["alg"])
		}
		kid, _ := token.Header["kid"].(string)
		// First attempt uses the cache; on failure the parser calls keyFunc
		// only once, so we force a refresh here if the cached lookup missed.
		key, err := getJWKSKey(jwksURL, kid, attempted)
		attempted = true
		return key, err
	}

	baseOpts := []jwt.ParserOption{
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512"}),
	}
	baseOpts = append(baseOpts, opts...)

	_, err := jwt.ParseWithClaims(tokenString, claims, keyFunc, baseOpts...)
	return err
}
