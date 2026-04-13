package auth

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash"
	"io"
	"math/big"
	"net/http"
	"strings"
	"sync"
	"time"
)

var (
	ErrNoBearer       = errors.New("no bearer token provided")
	ErrMalformedToken = errors.New("malformed JWT token")
	ErrTokenExpired   = errors.New("token has expired")
	ErrInvalidIssuer  = errors.New("invalid token issuer")
	ErrInvalidAud     = errors.New("invalid token audience")
	ErrInvalidSig     = errors.New("invalid token signature")
	ErrNoMatchingKey  = errors.New("no matching key found in JWKS")
	ErrUnsupportedAlg = errors.New("unsupported signing algorithm")
)

// OAuth2Options holds configuration for the OAuth2Provider.
type OAuth2Options struct {
	JWKSURL     string
	Issuer      string
	Audience    string
	UserIDClaim string
}

// jwksKey represents a single key in a JWKS response.
type jwksKey struct {
	Kty string `json:"kty"`
	Kid string `json:"kid"`
	Alg string `json:"alg"`
	Use string `json:"use"`
	N   string `json:"n"`
	E   string `json:"e"`
	Crv string `json:"crv"`
	X   string `json:"x"`
	Y   string `json:"y"`
}

// jwks represents a JSON Web Key Set.
type jwks struct {
	Keys []jwksKey `json:"keys"`
}

// jwtHeader is the decoded JWT header.
type jwtHeader struct {
	Alg string `json:"alg"`
	Kid string `json:"kid"`
	Typ string `json:"typ"`
}

// OAuth2Provider implements Provider using OAuth 2.0 Bearer tokens (JWT).
type OAuth2Provider struct {
	jwksURL     string
	issuer      string
	audience    string
	userIDClaim string

	mu        sync.RWMutex
	cachedJWKS *jwks
	httpClient *http.Client
}

// NewOAuth2Provider creates a new OAuth2Provider with the given options.
func NewOAuth2Provider(opts OAuth2Options) *OAuth2Provider {
	claim := opts.UserIDClaim
	if claim == "" {
		claim = "sub"
	}
	return &OAuth2Provider{
		jwksURL:     opts.JWKSURL,
		issuer:      opts.Issuer,
		audience:    opts.Audience,
		userIDClaim: claim,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}
}

// Authenticate extracts a Bearer token from the request, validates it as a JWT,
// and returns the authenticated user.
func (p *OAuth2Provider) Authenticate(r *http.Request) (*User, error) {
	token, err := extractBearerToken(r)
	if err != nil {
		return nil, err
	}

	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrMalformedToken
	}

	// Decode header
	headerBytes, err := base64URLDecode(parts[0])
	if err != nil {
		return nil, ErrMalformedToken
	}
	var header jwtHeader
	if err := json.Unmarshal(headerBytes, &header); err != nil {
		return nil, ErrMalformedToken
	}

	// Decode payload
	payloadBytes, err := base64URLDecode(parts[1])
	if err != nil {
		return nil, ErrMalformedToken
	}
	var claims map[string]any
	if err := json.Unmarshal(payloadBytes, &claims); err != nil {
		return nil, ErrMalformedToken
	}

	// Decode signature
	sigBytes, err := base64URLDecode(parts[2])
	if err != nil {
		return nil, ErrMalformedToken
	}

	// Verify signature
	signingInput := parts[0] + "." + parts[1]
	if err := p.verifySignature(header, []byte(signingInput), sigBytes); err != nil {
		return nil, err
	}

	// Validate claims
	if err := p.validateClaims(claims); err != nil {
		return nil, err
	}

	// Extract user
	user := p.extractUser(claims)
	return user, nil
}

// Challenge returns the WWW-Authenticate header value for Bearer auth.
func (p *OAuth2Provider) Challenge() string {
	return `Bearer realm="CalDAV"`
}

func extractBearerToken(r *http.Request) (string, error) {
	auth := r.Header.Get("Authorization")
	if auth == "" {
		return "", ErrNoBearer
	}
	const prefix = "Bearer "
	if !strings.HasPrefix(auth, prefix) {
		return "", ErrNoBearer
	}
	token := strings.TrimSpace(auth[len(prefix):])
	if token == "" {
		return "", ErrNoBearer
	}
	return token, nil
}

func (p *OAuth2Provider) verifySignature(header jwtHeader, signingInput, sig []byte) error {
	key, err := p.findKey(header.Kid)
	if err != nil {
		return err
	}

	switch header.Alg {
	case "RS256":
		return verifyRSA(key, crypto.SHA256, signingInput, sig)
	case "RS384":
		return verifyRSA(key, crypto.SHA384, signingInput, sig)
	case "RS512":
		return verifyRSA(key, crypto.SHA512, signingInput, sig)
	case "ES256":
		return verifyECDSA(key, crypto.SHA256, signingInput, sig, 32)
	case "ES384":
		return verifyECDSA(key, crypto.SHA384, signingInput, sig, 48)
	case "ES512":
		return verifyECDSA(key, crypto.SHA512, signingInput, sig, 66)
	default:
		return ErrUnsupportedAlg
	}
}

func verifyRSA(key *jwksKey, h crypto.Hash, signingInput, sig []byte) error {
	pubKey, err := rsaPublicKeyFromJWK(key)
	if err != nil {
		return err
	}
	hasher := newHash(h)
	hasher.Write(signingInput)
	hashed := hasher.Sum(nil)
	if err := rsa.VerifyPKCS1v15(pubKey, h, hashed, sig); err != nil {
		return ErrInvalidSig
	}
	return nil
}

func verifyECDSA(key *jwksKey, h crypto.Hash, signingInput, sig []byte, keySize int) error {
	pubKey, err := ecdsaPublicKeyFromJWK(key)
	if err != nil {
		return err
	}
	hasher := newHash(h)
	hasher.Write(signingInput)
	hashed := hasher.Sum(nil)

	// ECDSA signatures in JWTs are r || s, each padded to keySize bytes
	if len(sig) != 2*keySize {
		return ErrInvalidSig
	}
	r := new(big.Int).SetBytes(sig[:keySize])
	s := new(big.Int).SetBytes(sig[keySize:])

	if !ecdsa.Verify(pubKey, hashed, r, s) {
		return ErrInvalidSig
	}
	return nil
}

func newHash(h crypto.Hash) hash.Hash {
	switch h {
	case crypto.SHA256:
		return sha256.New()
	case crypto.SHA384:
		return sha512.New384()
	case crypto.SHA512:
		return sha512.New()
	default:
		return sha256.New()
	}
}

func rsaPublicKeyFromJWK(key *jwksKey) (*rsa.PublicKey, error) {
	nBytes, err := base64URLDecode(key.N)
	if err != nil {
		return nil, fmt.Errorf("invalid JWK n: %w", err)
	}
	eBytes, err := base64URLDecode(key.E)
	if err != nil {
		return nil, fmt.Errorf("invalid JWK e: %w", err)
	}
	n := new(big.Int).SetBytes(nBytes)
	e := new(big.Int).SetBytes(eBytes)
	return &rsa.PublicKey{
		N: n,
		E: int(e.Int64()),
	}, nil
}

func ecdsaPublicKeyFromJWK(key *jwksKey) (*ecdsa.PublicKey, error) {
	xBytes, err := base64URLDecode(key.X)
	if err != nil {
		return nil, fmt.Errorf("invalid JWK x: %w", err)
	}
	yBytes, err := base64URLDecode(key.Y)
	if err != nil {
		return nil, fmt.Errorf("invalid JWK y: %w", err)
	}

	var curve elliptic.Curve
	switch key.Crv {
	case "P-256":
		curve = elliptic.P256()
	case "P-384":
		curve = elliptic.P384()
	case "P-521":
		curve = elliptic.P521()
	default:
		return nil, fmt.Errorf("unsupported curve: %s", key.Crv)
	}

	x := new(big.Int).SetBytes(xBytes)
	y := new(big.Int).SetBytes(yBytes)
	return &ecdsa.PublicKey{
		Curve: curve,
		X:     x,
		Y:     y,
	}, nil
}

func (p *OAuth2Provider) findKey(kid string) (*jwksKey, error) {
	// Try cached JWKS first
	p.mu.RLock()
	cached := p.cachedJWKS
	p.mu.RUnlock()

	if cached != nil {
		if key := findKeyByID(cached, kid); key != nil {
			return key, nil
		}
	}

	// Fetch fresh JWKS
	fresh, err := p.fetchJWKS()
	if err != nil {
		return nil, fmt.Errorf("failed to fetch JWKS: %w", err)
	}

	p.mu.Lock()
	p.cachedJWKS = fresh
	p.mu.Unlock()

	if key := findKeyByID(fresh, kid); key != nil {
		return key, nil
	}
	return nil, ErrNoMatchingKey
}

func findKeyByID(ks *jwks, kid string) *jwksKey {
	for i := range ks.Keys {
		if ks.Keys[i].Kid == kid {
			return &ks.Keys[i]
		}
	}
	return nil
}

func (p *OAuth2Provider) fetchJWKS() (*jwks, error) {
	resp, err := p.httpClient.Get(p.jwksURL)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var ks jwks
	if err := json.Unmarshal(body, &ks); err != nil {
		return nil, err
	}
	return &ks, nil
}

func (p *OAuth2Provider) validateClaims(claims map[string]any) error {
	// Check expiration
	if exp, ok := claims["exp"]; ok {
		var expFloat float64
		switch v := exp.(type) {
		case float64:
			expFloat = v
		case json.Number:
			f, err := v.Float64()
			if err != nil {
				return ErrMalformedToken
			}
			expFloat = f
		default:
			return ErrMalformedToken
		}
		if time.Now().Unix() > int64(expFloat) {
			return ErrTokenExpired
		}
	}

	// Check issuer
	if p.issuer != "" {
		iss, _ := claims["iss"].(string)
		if iss != p.issuer {
			return ErrInvalidIssuer
		}
	}

	// Check audience
	if p.audience != "" {
		switch aud := claims["aud"].(type) {
		case string:
			if aud != p.audience {
				return ErrInvalidAud
			}
		case []any:
			found := false
			for _, a := range aud {
				if s, ok := a.(string); ok && s == p.audience {
					found = true
					break
				}
			}
			if !found {
				return ErrInvalidAud
			}
		default:
			return ErrInvalidAud
		}
	}

	return nil
}

func (p *OAuth2Provider) extractUser(claims map[string]any) *User {
	user := &User{}

	if id, ok := claims[p.userIDClaim].(string); ok {
		user.ID = id
	}
	if name, ok := claims["name"].(string); ok {
		user.DisplayName = name
	}
	if email, ok := claims["email"].(string); ok {
		user.Email = email
	}

	return user
}

// base64URLDecode decodes a base64url-encoded string (no padding).
func base64URLDecode(s string) ([]byte, error) {
	// Add padding if necessary
	switch len(s) % 4 {
	case 2:
		s += "=="
	case 3:
		s += "="
	}
	return base64.URLEncoding.DecodeString(s)
}
