package auth

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestBasicAuth_Success(t *testing.T) {
	p := NewBasicProvider()
	err := p.AddUser("alice", "secret123", User{
		ID:          "alice-id",
		DisplayName: "Alice",
		Email:       "alice@example.com",
	})
	if err != nil {
		t.Fatalf("AddUser failed: %v", err)
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("alice", "secret123")

	user, err := p.Authenticate(req)
	if err != nil {
		t.Fatalf("Authenticate failed: %v", err)
	}
	if user.ID != "alice-id" {
		t.Errorf("expected user ID alice-id, got %s", user.ID)
	}
	if user.DisplayName != "Alice" {
		t.Errorf("expected display name Alice, got %s", user.DisplayName)
	}
	if user.Email != "alice@example.com" {
		t.Errorf("expected email alice@example.com, got %s", user.Email)
	}
}

func TestBasicAuth_WrongPassword(t *testing.T) {
	p := NewBasicProvider()
	_ = p.AddUser("alice", "secret123", User{ID: "alice-id"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("alice", "wrongpassword")

	_, err := p.Authenticate(req)
	if err != ErrWrongPassword {
		t.Errorf("expected ErrWrongPassword, got %v", err)
	}
}

func TestBasicAuth_UnknownUser(t *testing.T) {
	p := NewBasicProvider()
	_ = p.AddUser("alice", "secret123", User{ID: "alice-id"})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.SetBasicAuth("bob", "secret123")

	_, err := p.Authenticate(req)
	if err != ErrUnknownUser {
		t.Errorf("expected ErrUnknownUser, got %v", err)
	}
}

func TestBasicAuth_NoHeader(t *testing.T) {
	p := NewBasicProvider()

	req := httptest.NewRequest(http.MethodGet, "/", nil)

	_, err := p.Authenticate(req)
	if err != ErrUnauthorized {
		t.Errorf("expected ErrUnauthorized, got %v", err)
	}
}

func TestBasicAuth_MalformedHeader(t *testing.T) {
	p := NewBasicProvider()

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Basic not-valid-base64!!!")

	_, err := p.Authenticate(req)
	if err != ErrInvalidHeader {
		t.Errorf("expected ErrInvalidHeader, got %v", err)
	}
}

func TestChallenge(t *testing.T) {
	p := NewBasicProvider()
	expected := `Basic realm="CalDAV"`
	if got := p.Challenge(); got != expected {
		t.Errorf("expected challenge %q, got %q", expected, got)
	}
}
