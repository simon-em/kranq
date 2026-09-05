package token

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
)

const (
	Prefix     = "forge_"
	secretSize = 24
	// Enough to tell two tokens apart in a list without being enough to guess
	// one from a log line.
	displayLen = 8
)

var (
	ErrNotFound = errors.New("no such token")
	ErrExists   = errors.New("a token by that name already exists")
)

var namePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

type Token struct {
	Name     string     `json:"name"`
	Display  string     `json:"display"`
	Hash     string     `json:"hash"`
	Created  time.Time  `json:"created"`
	LastUsed *time.Time `json:"last_used,omitempty"`
}

type Set struct {
	Tokens []Token `json:"tokens"`
}

func Path(home string) string { return filepath.Join(home, "tokens.json") }

func Load(path string) (*Set, error) {
	s := &Set{}
	body, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, s); err != nil {
		return nil, fmt.Errorf("%s is not readable as a token list: %w", path, err)
	}
	return s, nil
}

func (s *Set) Save(path string) error {
	body, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(body, '\n'), 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Create returns the secret exactly once. Only its hash is stored, so a stolen
// tokens.json cannot be used to authenticate.
func (s *Set) Create(name string, now time.Time) (string, error) {
	if !namePattern.MatchString(name) {
		return "", fmt.Errorf("%q is not a usable token name: lowercase letters, digits and dashes", name)
	}
	for _, t := range s.Tokens {
		if t.Name == name {
			return "", fmt.Errorf("%w: %s", ErrExists, name)
		}
	}
	raw := make([]byte, secretSize)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	secret := Prefix + hex.EncodeToString(raw)
	s.Tokens = append(s.Tokens, Token{
		Name:    name,
		Display: secret[:len(Prefix)+displayLen],
		Hash:    hashOf(secret),
		Created: now.UTC().Truncate(time.Second),
	})
	sort.Slice(s.Tokens, func(i, j int) bool { return s.Tokens[i].Name < s.Tokens[j].Name })
	return secret, nil
}

func (s *Set) Revoke(name string) error {
	for i, t := range s.Tokens {
		if t.Name == name {
			s.Tokens = append(s.Tokens[:i], s.Tokens[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("%w: %s", ErrNotFound, name)
}

// Verify compares every candidate in constant time and without returning early,
// so the time it takes reveals neither which token matched nor how much of a
// wrong one was right.
func (s *Set) Verify(secret string) (string, bool) {
	want := hashOf(secret)
	name, found := "", false
	for _, t := range s.Tokens {
		if subtle.ConstantTimeCompare([]byte(t.Hash), []byte(want)) == 1 {
			name, found = t.Name, true
		}
	}
	if !strings.HasPrefix(secret, Prefix) {
		return "", false
	}
	return name, found
}

func (s *Set) Touch(name string, now time.Time) {
	at := now.UTC().Truncate(time.Second)
	for i := range s.Tokens {
		if s.Tokens[i].Name == name {
			s.Tokens[i].LastUsed = &at
			return
		}
	}
}

func hashOf(secret string) string {
	sum := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(sum[:])
}
