package image

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const maxRecordedRepos = 10

// Meta is annotation, never truth. Lima is the record of what exists; this only
// says what a layer was for and when it last mattered, which is what prune
// needs and what a hash cannot carry.
type Meta struct {
	Name       string    `json:"name"`
	Parent     string    `json:"parent"`
	Depth      int       `json:"depth"`
	Step       string    `json:"step"`
	Copies     []string  `json:"copies,omitempty"`
	Repos      []string  `json:"repos,omitempty"`
	CreatedAt  time.Time `json:"created_at"`
	LastUsedAt time.Time `json:"last_used_at"`
}

type metaStore struct{ dir string }

func (s metaStore) path(name string) string {
	return filepath.Join(s.dir, name+".json")
}

func (s metaStore) load(name string) (Meta, bool) {
	if s.dir == "" {
		return Meta{}, false
	}
	raw, err := os.ReadFile(s.path(name))
	if err != nil {
		return Meta{}, false
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		return Meta{}, false
	}
	return m, true
}

func (s metaStore) all() map[string]Meta {
	out := map[string]Meta{}
	if s.dir == "" {
		return out
	}
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return out
	}
	for _, e := range entries {
		name := strings.TrimSuffix(e.Name(), ".json")
		if name == e.Name() {
			continue
		}
		if m, ok := s.load(name); ok {
			out[name] = m
		}
	}
	return out
}

func (s metaStore) save(m Meta) error {
	if s.dir == "" {
		return nil
	}
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := s.path(m.Name) + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, s.path(m.Name))
}

func (s metaStore) remove(name string) {
	if s.dir != "" {
		_ = os.Remove(s.path(name))
	}
}

func (s metaStore) touch(name, repo string, now time.Time) {
	m, ok := s.load(name)
	if !ok {
		return
	}
	m.LastUsedAt = now
	m.Repos = withRepo(m.Repos, repo)
	_ = s.save(m)
}

func withRepo(repos []string, repo string) []string {
	if repo == "" {
		return repos
	}
	for _, r := range repos {
		if r == repo {
			return repos
		}
	}
	repos = append(repos, repo)
	sort.Strings(repos)
	if len(repos) > maxRecordedRepos {
		repos = repos[:maxRecordedRepos]
	}
	return repos
}
