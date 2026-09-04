package state

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"
)

var ErrNotFound = errors.New("no such task")

type Store struct {
	mu    sync.RWMutex
	dir   string
	index map[string]Task
}

func Open(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	s := &Store{dir: dir, index: map[string]Task{}}
	return s, s.reload()
}

func NewID(now time.Time) string {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return now.UTC().Format("20060102T150405") + "-" + hex.EncodeToString(b)
}

func (s *Store) Dir(id string) string          { return filepath.Join(s.dir, id) }
func (s *Store) LogPath(id string) string      { return filepath.Join(s.Dir(id), "log") }
func (s *Store) ArtifactsDir(id string) string { return filepath.Join(s.Dir(id), "artifacts") }
func (s *Store) secretsPath(id string) string  { return filepath.Join(s.Dir(id), "secrets.json") }

func (s *Store) reload() error {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		t, err := s.readFromDisk(e.Name())
		if err != nil {
			continue
		}
		s.index[t.ID] = t
	}
	return nil
}

func (s *Store) readFromDisk(id string) (Task, error) {
	var t Task
	data, err := os.ReadFile(filepath.Join(s.Dir(id), "task.json"))
	if err != nil {
		if os.IsNotExist(err) {
			return t, ErrNotFound
		}
		return t, err
	}
	if err := json.Unmarshal(data, &t); err != nil {
		return t, fmt.Errorf("corrupt task record %s: %w", id, err)
	}
	if env, err := os.ReadFile(s.secretsPath(id)); err == nil {
		_ = json.Unmarshal(env, &t.Env)
	}
	return t, nil
}

func (s *Store) Create(t Task) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.index[t.ID]; ok {
		return fmt.Errorf("task %s already exists", t.ID)
	}
	return s.write(t)
}

func (s *Store) write(t Task) error {
	dir := s.Dir(t.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if len(t.Env) > 0 {
		env, err := json.Marshal(t.Env)
		if err != nil {
			return err
		}
		if err := writeAtomic(s.secretsPath(t.ID), env); err != nil {
			return err
		}
	}
	data, err := json.MarshalIndent(t, "", "  ")
	if err != nil {
		return err
	}
	if err := writeAtomic(filepath.Join(dir, "task.json"), data); err != nil {
		return err
	}
	s.index[t.ID] = t
	return nil
}

func writeAtomic(path string, data []byte) error {
	tmp, err := os.CreateTemp(filepath.Dir(path), filepath.Base(path)+".*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}

func (s *Store) Get(id string) (Task, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	t, ok := s.index[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	return t, nil
}

func (s *Store) Update(id string, fn func(*Task)) (Task, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	t, ok := s.index[id]
	if !ok {
		return Task{}, ErrNotFound
	}
	fn(&t)
	if err := s.write(t); err != nil {
		return t, err
	}
	return t, nil
}

func (s *Store) List() []Task {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Task, 0, len(s.index))
	for _, t := range s.index {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

func (s *Store) Prune(olderThan time.Duration, now time.Time) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	cutoff := now.Add(-olderThan)
	removed := 0
	for id, t := range s.index {
		if !t.Terminal() || t.CreatedAt.After(cutoff) {
			continue
		}
		if os.RemoveAll(s.Dir(id)) == nil {
			delete(s.index, id)
			removed++
		}
	}
	return removed
}
