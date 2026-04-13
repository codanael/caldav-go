package auth

import (
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// testKeyID is the kid used in test JWTs.
const testKeyID = "test-key-1"

// generateTestKey creates an RSA key pair for testing.
func generateTestKey(t *testing.T) *rsa.PrivateKey {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatalf("failed to generate RSA key: %v", err)
	}
	return key
}

// serveJWKS starts an httptest server that serves a JWKS containing the given RSA public key.
func serveJWKS(t *testing.T, key *rsa.PublicKey) *httptest.Server {
	t.Helper()

	nBytes := key.N.Bytes()
	eBytes := intToBytes(key.E)

	jwksResp := map[string]any{
		"keys": []map[string]any{
			{
				"kty": "RSA",
				"kid": testKeyID,
				"alg": "RS256",
				"use": "sig",
				"n":   base64.RawURLEncoding.EncodeToString(nBytes),
				"e":   base64.RawURLEncoding.EncodeToString(eBytes),
			},
		},
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(jwksResp)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// intToBytes converts an int to big-endian bytes.
func intToBytes(i int) []byte {
	if i == 0 {
		return []byte{0}
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte(i & 0xff)}, b...)
		i >>= 8
	}
	return b
}

// signJWT creates a signed JWT with the given claims using RS256.
func signJWT(t *testing.T, key *rsa.PrivateKey, claims map[string]any) string {
	t.Helper()

	header := map[string]any{
		"alg": "RS256",
		"typ": "JWT",
		"kid": testKeyID,
	}

	headerJSON, err := json.Marshal(header)
	if err != nil {
		t.Fatalf("failed to marshal header: %v", err)
	}
	payloadJSON, err := json.Marshal(claims)
	if err != nil {
		t.Fatalf("failed to marshal claims: %v", err)
	}

	headerB64 := base64.RawURLEncoding.EncodeToString(headerJSON)
	payloadB64 := base64.RawURLEncoding.EncodeToString(payloadJSON)

	signingInput := headerB64 + "." + payloadB64
	hasher := sha256.New()
	hasher.Write([]byte(signingInput))
	hashed := hasher.Sum(nil)

	sig, err := rsa.SignPKCS1v15(rand.Reader, key, crypto.SHA256, hashed)
	if err != nil {
		t.Fatalf("failed to sign JWT: %v", err)
	}

	sigB64 := base64.RawURLEncoding.EncodeToString(sig)
	return fmt.Sprintf("%s.%s.%s", headerB64, payloadB64, sigB64)
}

func TestOAuth2_ValidToken(t *testing.T) {
	key := generateTestKey(t)
	srv := serveJWKS(t, &key.PublicKey)

	provider := NewOAuth2Provider(OAuth2Options{
		JWKSURL:  srv.URL,
		Issuer:   "https://auth.example.com",
		Audience: "caldav-server",
	})

	token := signJWT(t, key, map[string]any{
		"sub":   "user-42",
		"name":  "Test User",
		"email": "test@example.com",
		"iss":   "https://auth.example.com",
		"aud":   "caldav-server",
		"exp":   float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	user, err := provider.Authenticate(req)
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if user.ID != "user-42" {
		t.Errorf("expected user ID user-42, got %s", user.ID)
	}
	if user.DisplayName != "Test User" {
		t.Errorf("expected display name Test User, got %s", user.DisplayName)
	}
	if user.Email != "test@example.com" {
		t.Errorf("expected email test@example.com, got %s", user.Email)
	}
}

func TestOAuth2_ExpiredToken(t *testing.T) {
	key := generateTestKey(t)
	srv := serveJWKS(t, &key.PublicKey)

	provider := NewOAuth2Provider(OAuth2Options{
		JWKSURL:  srv.URL,
		Issuer:   "https://auth.example.com",
		Audience: "caldav-server",
	})

	token := signJWT(t, key, map[string]any{
		"sub": "user-42",
		"iss": "https://auth.example.com",
		"aud": "caldav-server",
		"exp": float64(time.Now().Add(-1 * time.Hour).Unix()),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	_, err := provider.Authenticate(req)
	if err != ErrTokenExpired {
		t.Errorf("expected ErrTokenExpired, got %v", err)
	}
}

func TestOAuth2_WrongIssuer(t *testing.T) {
	key := generateTestKey(t)
	srv := serveJWKS(t, &key.PublicKey)

	provider := NewOAuth2Provider(OAuth2Options{
		JWKSURL:  srv.URL,
		Issuer:   "https://auth.example.com",
		Audience: "caldav-server",
	})

	token := signJWT(t, key, map[string]any{
		"sub": "user-42",
		"iss": "https://evil.example.com",
		"aud": "caldav-server",
		"exp": float64(time.Now().Add(1 * time.Hour).Unix()),
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)

	_, err := provider.Authenticate(req)
	if err != ErrInvalidIssuer {
		t.Errorf("expected ErrInvalidIssuer, got %v", err)
	}
}

func TestOAuth2_NoBearer(t *testing.T) {
	provider := NewOAuth2Provider(OAuth2Options{
		JWKSURL: "http://localhost/jwks",
	})

	// No Authorization header at all
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	_, err := provider.Authenticate(req)
	if err != ErrNoBearer {
		t.Errorf("expected ErrNoBearer, got %v", err)
	}

	// Basic auth instead of Bearer
	req2 := httptest.NewRequest(http.MethodGet, "/", nil)
	req2.Header.Set("Authorization", "Basic dXNlcjpwYXNz")
	_, err = provider.Authenticate(req2)
	if err != ErrNoBearer {
		t.Errorf("expected ErrNoBearer for Basic auth header, got %v", err)
	}
}

func TestOAuth2_MalformedToken(t *testing.T) {
	provider := NewOAuth2Provider(OAuth2Options{
		JWKSURL: "http://localhost/jwks",
	})

	tests := []struct {
		name  string
		token string
	}{
		{"no dots", "justabunchoftext"},
		{"one dot", "part1.part2"},
		{"empty parts", ".."},
		{"invalid base64 header", "!!!.eyJ0ZXN0IjoxfQ.sig"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set("Authorization", "Bearer "+tt.token)

			_, err := provider.Authenticate(req)
			if err != ErrMalformedToken {
				t.Errorf("expected ErrMalformedToken, got %v", err)
			}
		})
	}
}

func TestOAuth2_Challenge(t *testing.T) {
	provider := NewOAuth2Provider(OAuth2Options{})
	expected := `Bearer realm="CalDAV"`
	if got := provider.Challenge(); got != expected {
		t.Errorf("expected challenge %q, got %q", expected, got)
	}
}
