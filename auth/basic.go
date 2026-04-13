package auth

import (
	"errors"
	"net/http"
	"sync"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrUnauthorized    = errors.New("unauthorized")
	ErrInvalidHeader   = errors.New("invalid authorization header")
	ErrUnknownUser     = errors.New("unknown user")
	ErrWrongPassword   = errors.New("wrong password")
	ErrUserExists      = errors.New("user already exists")
)

// basicUserRecord holds a bcrypt-hashed password and the associated user info.
type basicUserRecord struct {
	hashedPassword []byte
	user           User
}

// BasicProvider implements Provider using HTTP Basic authentication with
// bcrypt-hashed passwords stored in memory.
type BasicProvider struct {
	mu    sync.RWMutex
	users map[string]*basicUserRecord
}

// NewBasicProvider creates a new BasicProvider with an empty user store.
func NewBasicProvider() *BasicProvider {
	return &BasicProvider{
		users: make(map[string]*basicUserRecord),
	}
}

// AddUser registers a user with the given username and plaintext password.
// The password is hashed with bcrypt before storage.
func (p *BasicProvider) AddUser(username, password string, user User) error {
	p.mu.Lock()
	defer p.mu.Unlock()

	if _, exists := p.users[username]; exists {
		return ErrUserExists
	}

	hashed, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return err
	}

	p.users[username] = &basicUserRecord{
		hashedPassword: hashed,
		user:           user,
	}
	return nil
}

// Authenticate extracts Basic credentials from the request and validates them.
func (p *BasicProvider) Authenticate(r *http.Request) (*User, error) {
	username, password, ok := r.BasicAuth()
	if !ok {
		header := r.Header.Get("Authorization")
		if header == "" {
			return nil, ErrUnauthorized
		}
		return nil, ErrInvalidHeader
	}

	p.mu.RLock()
	record, exists := p.users[username]
	p.mu.RUnlock()

	if !exists {
		return nil, ErrUnknownUser
	}

	if err := bcrypt.CompareHashAndPassword(record.hashedPassword, []byte(password)); err != nil {
		return nil, ErrWrongPassword
	}

	u := record.user
	return &u, nil
}

// Challenge returns the WWW-Authenticate header value for Basic auth.
func (p *BasicProvider) Challenge() string {
	return `Basic realm="CalDAV"`
}
