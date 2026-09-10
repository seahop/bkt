package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
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
	keys    map[string]crypto.PublicKey // kid -> *rsa.PublicKey or *ecdsa.PublicKey ("" kid allowed for single-key sets)
	fetched time.Time
}

var (
	jwksMu    sync.Mutex
	jwksStore = map[string]cachedKeySet{}
)

// getJWKSKey returns the public key (RSA or EC) for the given kid from the JWKS at url,
// using a short-lived cache. If the kid isn't cached (or forceRefresh is set) it
// refetches once — this both bootstraps the cache and picks up key rotations.
func getJWKSKey(url, kid string, forceRefresh bool) (crypto.PublicKey, error) {
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

func lookupKey(keys map[string]crypto.PublicKey, kid string) (crypto.PublicKey, bool) {
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
	Use string   `json:"use"`
	N   string   `json:"n"`
	E   string   `json:"e"`
	Crv string   `json:"crv"`
	X   string   `json:"x"`
	Y   string   `json:"y"`
	X5c []string `json:"x5c"`
}

type jwkSet struct {
	Keys []jwkKey `json:"keys"`
}

func fetchJWKS(url string) (map[string]crypto.PublicKey, error) {
	resp, err := jwksHTTPClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck // best-effort close of response body
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("failed to fetch JWKS: status %s", resp.Status)
	}

	var set jwkSet
	if err := json.NewDecoder(resp.Body).Decode(&set); err != nil {
		return nil, fmt.Errorf("failed to decode JWKS: %w", err)
	}

	keys := make(map[string]crypto.PublicKey)
	for _, k := range set.Keys {
		// Encryption-only keys must never be used to verify signatures.
		if k.Use != "" && k.Use != "sig" {
			continue
		}
		var (
			pub crypto.PublicKey
			err error
		)
		switch k.Kty {
		case "", "RSA":
			pub, err = jwkToRSAPublicKey(k)
		case "EC":
			pub, err = jwkToECPublicKey(k)
		default:
			continue
		}
		if err != nil {
			continue // skip unusable keys rather than failing the whole set
		}
		keys[k.Kid] = pub
	}
	if len(keys) == 0 {
		return nil, fmt.Errorf("JWKS contained no usable RSA or EC signing keys")
	}
	return keys, nil
}

// jwkToECPublicKey converts an EC JWK (P-256/P-384/P-521, as used by ES256/384/512
// signers such as Kanidm, Authentik and some Okta/Entra configurations) into an
// ecdsa.PublicKey, validating that the point is on the curve.
func jwkToECPublicKey(k jwkKey) (*ecdsa.PublicKey, error) {
	var curve elliptic.Curve
	switch k.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("unsupported EC curve %q", k.Crv)
	}
	xb, err := base64.RawURLEncoding.DecodeString(k.X)
	if err != nil {
		return nil, err
	}
	yb, err := base64.RawURLEncoding.DecodeString(k.Y)
	if err != nil {
		return nil, err
	}
	// Assemble the SEC 1 uncompressed point (0x04 || X || Y) with each
	// coordinate left-padded to the curve size; the parser performs the
	// on-curve check.
	size := (curve.Params().BitSize + 7) / 8
	if len(xb) > size || len(yb) > size {
		return nil, fmt.Errorf("EC coordinate too large for curve %s", k.Crv)
	}
	point := make([]byte, 1+2*size)
	point[0] = 4
	copy(point[1+size-len(xb):], xb)
	copy(point[1+2*size-len(yb):], yb)
	pub, err := ecdsa.ParseUncompressedPublicKey(curve, point)
	if err != nil {
		return nil, fmt.Errorf("invalid EC public key for %s: %w", k.Crv, err)
	}
	return pub, nil
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
// JWKS at jwksURL, populating claims. It pins the signing algorithm to the
// asymmetric RSA/ECDSA families (so alg:none and HMAC confusion are rejected —
// a symmetric alg would let an attacker sign with the public key) and applies any extra
// parser options (audience, issuer, expiration-required). On a kid cache miss it
// refetches the JWKS once before giving up.
func verifyJWTWithJWKS(tokenString, jwksURL string, claims jwt.Claims, opts ...jwt.ParserOption) error {
	if jwksURL == "" {
		return fmt.Errorf("no JWKS URL configured; cannot verify token signature")
	}

	attempted := false
	keyFunc := func(token *jwt.Token) (interface{}, error) {
		switch token.Method.(type) {
		case *jwt.SigningMethodRSA, *jwt.SigningMethodECDSA:
		default:
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
		jwt.WithValidMethods([]string{"RS256", "RS384", "RS512", "ES256", "ES384", "ES512"}),
	}
	baseOpts = append(baseOpts, opts...)

	_, err := jwt.ParseWithClaims(tokenString, claims, keyFunc, baseOpts...)
	return err
}
