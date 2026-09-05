package token

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

var now = time.Date(2026, 9, 4, 12, 0, 0, 0, time.UTC)

func TestCreateReturnsASecretThatVerifies(t *testing.T) {
	s := &Set{}
	secret, err := s.Create("ci-dx", now)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(secret, Prefix) {
		t.Fatalf("secret %q has no prefix to recognise it by", secret)
	}
	name, ok := s.Verify(secret)
	if !ok || name != "ci-dx" {
		t.Fatalf("verify returned %q %v", name, ok)
	}
}

// A stolen tokens.json must not be usable to authenticate.
func TestTheSecretIsNeverStored(t *testing.T) {
	s := &Set{}
	secret, err := s.Create("ci-dx", now)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(body), secret) {
		t.Fatal("the secret was written to disk")
	}
	// The displayed prefix is short enough to be useless on its own.
	if len(s.Tokens[0].Display) >= len(secret) {
		t.Fatal("the whole secret is being displayed")
	}
	if !strings.Contains(string(body), s.Tokens[0].Display) {
		t.Fatal("nothing identifies the token in a listing")
	}
}

func TestTokensFileIsPrivate(t *testing.T) {
	s := &Set{}
	if _, err := s.Create("ci-dx", now); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "tokens.json")
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Fatalf("tokens.json is %04o", info.Mode().Perm())
	}
}

func TestWrongSecretsAreRefused(t *testing.T) {
	s := &Set{}
	secret, _ := s.Create("ci-dx", now)
	for _, bad := range []string{
		"", "wrong", secret + "x", secret[:len(secret)-1],
		strings.TrimPrefix(secret, Prefix), Prefix,
	} {
		if _, ok := s.Verify(bad); ok {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestTwoTokensAreDistinct(t *testing.T) {
	s := &Set{}
	a, _ := s.Create("ci-dx", now)
	b, err := s.Create("ci-other", now)
	if err != nil {
		t.Fatal(err)
	}
	if a == b {
		t.Fatal("two tokens came out the same")
	}
	if name, _ := s.Verify(a); name != "ci-dx" {
		t.Fatalf("token a verified as %q", name)
	}
	if name, _ := s.Verify(b); name != "ci-other" {
		t.Fatalf("token b verified as %q", name)
	}
}

func TestRevokeStopsATokenWorking(t *testing.T) {
	s := &Set{}
	secret, _ := s.Create("ci-dx", now)
	if err := s.Revoke("ci-dx"); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify(secret); ok {
		t.Fatal("a revoked token still works")
	}
	if err := s.Revoke("ci-dx"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDuplicateNamesAreRefused(t *testing.T) {
	s := &Set{}
	if _, err := s.Create("ci-dx", now); err != nil {
		t.Fatal(err)
	}
	// Silently replacing it would revoke the working token of whoever else is
	// using that name.
	if _, err := s.Create("ci-dx", now); !errors.Is(err, ErrExists) {
		t.Fatalf("expected ErrExists, got %v", err)
	}
	if len(s.Tokens) != 1 {
		t.Fatalf("%d tokens", len(s.Tokens))
	}
}

func TestBadNamesAreRefused(t *testing.T) {
	s := &Set{}
	for _, bad := range []string{"", "CI", "ci_dx", "-ci", strings.Repeat("c", 33), "ci dx"} {
		if _, err := s.Create(bad, now); err == nil {
			t.Fatalf("%q was accepted", bad)
		}
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "tokens.json")
	s := &Set{}
	secret, _ := s.Create("ci-dx", now)
	if err := s.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if name, ok := back.Verify(secret); !ok || name != "ci-dx" {
		t.Fatalf("the token did not survive a round trip: %q %v", name, ok)
	}
}

func TestLoadingNothingIsNotAnError(t *testing.T) {
	s, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Verify("anything"); ok {
		t.Fatal("an empty set verified something")
	}
}

func TestTouchRecordsUse(t *testing.T) {
	s := &Set{}
	s.Create("ci-dx", now)
	s.Touch("ci-dx", now.Add(time.Hour))
	if s.Tokens[0].LastUsed == nil {
		t.Fatal("use was not recorded")
	}
	s.Touch("absent", now)
}
