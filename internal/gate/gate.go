package gate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

type State struct {
	TokenID     string        `json:"token_id"`
	Present     bool          `json:"present"`
	Available   bool          `json:"available"`
	Remaining   time.Duration `json:"remaining"`
	Exhaustions int           `json:"exhaustions"`
	LastReason  string        `json:"last_reason,omitempty"`
}

type persisted struct {
	TokenID     string    `json:"token_id"`
	Until       time.Time `json:"until"`
	Exhaustions int       `json:"exhaustions"`
	LastReason  string    `json:"last_reason,omitempty"`
}

type Gate struct {
	mu      sync.Mutex
	path    string
	tokenID string
	present bool
	retry   time.Duration
	now     func() time.Time

	until       time.Time
	exhaustions int
	reason      string
}

func TokenID(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

const DefaultRetry = time.Minute

func New(path, token string, retry time.Duration) *Gate {
	if retry <= 0 {
		retry = DefaultRetry
	}
	g := &Gate{
		path:    path,
		tokenID: TokenID(token),
		present: token != "",
		retry:   retry,
		now:     time.Now,
	}
	g.load()
	return g
}

func (g *Gate) load() {
	data, err := os.ReadFile(g.path)
	if err != nil {
		return
	}
	var p persisted
	if err := json.Unmarshal(data, &p); err != nil {
		return
	}
	if p.TokenID != g.tokenID {
		return
	}
	g.until = p.Until
	g.exhaustions = p.Exhaustions
	g.reason = p.LastReason
}

func (g *Gate) save() {
	if g.path == "" {
		return
	}
	data, err := json.Marshal(persisted{
		TokenID:     g.tokenID,
		Until:       g.until,
		Exhaustions: g.exhaustions,
		LastReason:  g.reason,
	})
	if err != nil {
		return
	}
	_ = os.MkdirAll(filepath.Dir(g.path), 0o700)
	_ = os.WriteFile(g.path, data, 0o600)
}

func (g *Gate) Present() bool { return g.present }

func (g *Gate) Available() bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	return !g.now().Before(g.until)
}

func (g *Gate) MarkExhausted() time.Time {
	return g.MarkExhaustedUntil(time.Time{}, "")
}

func (g *Gate) MarkExhaustedUntil(resetsAt time.Time, reason string) time.Time {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.exhaustions++
	g.reason = reason
	next := g.now().Add(g.retry)
	if !resetsAt.IsZero() && resetsAt.After(next) {
		next = resetsAt
	}
	g.until = next
	g.save()
	return g.until
}

func (g *Gate) MarkHealthy() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.until.IsZero() && g.reason == "" {
		return
	}
	g.until = time.Time{}
	g.reason = ""
	g.save()
}

func (g *Gate) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.until = time.Time{}
	g.exhaustions = 0
	g.reason = ""
	g.save()
}

func (g *Gate) State() State {
	g.mu.Lock()
	defer g.mu.Unlock()
	remaining := time.Duration(0)
	if d := g.until.Sub(g.now()); d > 0 {
		remaining = d
	}
	return State{
		TokenID:     g.tokenID,
		Present:     g.present,
		Available:   remaining == 0,
		Remaining:   remaining,
		Exhaustions: g.exhaustions,
		LastReason:  g.reason,
	}
}
