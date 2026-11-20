package keychain

import (
	"errors"
	"testing"

	"github.com/zalando/go-keyring"
)

func TestStore(t *testing.T) {
	keyring.MockInit()
	s := New("Goscriber test")
	if v, err := s.Get("http://localhost:11434/v1"); err != nil || v != "" {
		t.Fatalf("Get with nothing stored = %q, %v; want \"\", nil", v, err)
	}
	if err := s.Set("https://api.openai.com/v1", "sk-test"); err != nil {
		t.Fatal(err)
	}
	if v, err := keyring.Get("Goscriber test", "https://api.openai.com/v1"); err != nil || v != "sk-test" {
		t.Errorf("the OS store holds %q, %v; want the key", v, err)
	}
	// A fresh store reads it from the OS.
	if v, err := New("Goscriber test").Get("https://api.openai.com/v1"); err != nil || v != "sk-test" {
		t.Errorf("Get = %q, %v; want the key", v, err)
	}
	if err := s.Set("https://api.openai.com/v1", ""); err != nil {
		t.Fatal(err)
	}
	if _, err := keyring.Get("Goscriber test", "https://api.openai.com/v1"); !errors.Is(err, keyring.ErrNotFound) {
		t.Errorf("after deleting, the OS store answers %v; want not found", err)
	}
	if v, err := s.Get("https://api.openai.com/v1"); err != nil || v != "" {
		t.Errorf("Get after deleting = %q, %v", v, err)
	}
	// Deleting what is not there is no error.
	if err := s.Set("http://nowhere/v1", ""); err != nil {
		t.Errorf("deleting a missing key: %v", err)
	}
}

func TestStoreErrors(t *testing.T) {
	keyring.MockInitWithError(errors.New("no secret service"))
	s := New("Goscriber test")
	if _, err := s.Get("a"); err == nil {
		t.Error("Get succeeded with the OS store failing")
	}
	if err := s.Set("a", "secret"); err == nil {
		t.Error("Set succeeded with the OS store failing")
	}
}
