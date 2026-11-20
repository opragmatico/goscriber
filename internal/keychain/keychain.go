// Package keychain keeps secrets, such as the API keys of LLM providers,
// in the OS's credential store rather than in settings.json: the macOS
// Keychain, the Secret Service on Linux (GNOME Keyring, KWallet) and the
// Windows Credential Manager.
package keychain

import (
	"errors"
	"fmt"
	"sync"

	"github.com/zalando/go-keyring"
)

// Store reads and writes the secrets of one service, each under an
// account name, and remembers what it read, so a dictation does not wait
// on the OS for a key it already asked for. It is safe for concurrent use.
type Store struct {
	service string

	mu    sync.Mutex
	cache map[string]string
}

// New returns the store of service, the name the OS shows the secrets
// under, such as "Goscriber LLM API key".
func New(service string) *Store {
	return &Store{service: service, cache: map[string]string{}}
}

// Get returns the secret stored for account, or "" when there is none.
func (s *Store) Get(account string) (string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if v, ok := s.cache[account]; ok {
		return v, nil
	}
	v, err := keyring.Get(s.service, account)
	if errors.Is(err, keyring.ErrNotFound) {
		v, err = "", nil
	}
	if err != nil {
		return "", fmt.Errorf("keychain: read %s: %w", account, err)
	}
	s.cache[account] = v
	return v, nil
}

// Set stores secret for account, replacing what was there; "" deletes it.
func (s *Store) Set(account, secret string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	var err error
	if secret == "" {
		err = keyring.Delete(s.service, account)
		if errors.Is(err, keyring.ErrNotFound) {
			err = nil
		}
	} else {
		err = keyring.Set(s.service, account, secret)
	}
	if err != nil {
		delete(s.cache, account)
		return fmt.Errorf("keychain: store %s: %w", account, err)
	}
	s.cache[account] = secret
	return nil
}
