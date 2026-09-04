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
	Backoff     time.Duration `json:"backoff"`
	Exhaustions int           `json:"exhaustions"`
}

type persisted struct {
	TokenID     string    `json:"token_id"`
	Until       time.Time `json:"until"`
	Backoff     time.Duration
	Exhaustions int `json:"exhaustions"`
}

type Gate struct {
	mu      sync.Mutex
	path    string
	tokenID string
	present bool
	base    time.Duration
	max     time.Duration
	now     func() time.Time

	until       time.Time
	backoff     time.Duration
	exhaustions int
}

func TokenID(token string) string {
	if token == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(token))
	return hex.EncodeToString(sum[:8])
}

func New(path, token string, base, max time.Duration) *Gate {
	g := &Gate{
		path:    path,
		tokenID: TokenID(token),
		present: token != "",
		base:    base,
		max:     max,
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
	g.backoff = p.Backoff
	g.exhaustions = p.Exhaustions
}

func (g *Gate) save() {
	if g.path == "" {
		return
	}
	data, err := json.Marshal(persisted{
		TokenID:     g.tokenID,
		Until:       g.until,
		Backoff:     g.backoff,
		Exhaustions: g.exhaustions,
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
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.backoff == 0 {
		g.backoff = g.base
	} else {
		g.backoff *= 2
	}
	if g.backoff > g.max {
		g.backoff = g.max
	}
	g.exhaustions++
	g.until = g.now().Add(g.backoff)
	g.save()
	return g.until
}

func (g *Gate) MarkHealthy() {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.backoff == 0 && g.until.IsZero() {
		return
	}
	g.backoff = 0
	g.until = time.Time{}
	g.save()
}

func (g *Gate) Reset() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.backoff = 0
	g.until = time.Time{}
	g.exhaustions = 0
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
		Backoff:     g.backoff,
		Exhaustions: g.exhaustions,
	}
}
