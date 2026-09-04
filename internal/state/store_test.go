package state

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func store(t *testing.T) (*Store, string) {
	t.Helper()
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return s, dir
}

func task(id string, at time.Time) Task {
	return Task{ID: id, Name: "spec", Repo: "dx", Branch: "main", Status: StatusQueued, CreatedAt: at}
}

func TestCreateGetUpdate(t *testing.T) {
	s, _ := store(t)
	now := time.Now()
	if err := s.Create(task("a", now)); err != nil {
		t.Fatal(err)
	}
	got, err := s.Get("a")
	if err != nil || got.Repo != "dx" {
		t.Fatalf("Get = %+v, %v", got, err)
	}
	updated, err := s.Update("a", func(u *Task) { u.Status = StatusRunning })
	if err != nil || updated.Status != StatusRunning {
		t.Fatalf("Update = %+v, %v", updated, err)
	}
	again, _ := s.Get("a")
	if again.Status != StatusRunning {
		t.Error("the update did not stick")
	}
}

func TestUnknownIDsAreDistinguishable(t *testing.T) {
	s, _ := store(t)
	if _, err := s.Get("nope"); err != ErrNotFound {
		t.Errorf("Get = %v, want ErrNotFound so a caller can 404 rather than 500", err)
	}
	if _, err := s.Update("nope", func(*Task) {}); err != ErrNotFound {
		t.Errorf("Update = %v, want ErrNotFound", err)
	}
}

func TestCreateRefusesToOverwrite(t *testing.T) {
	s, _ := store(t)
	now := time.Now()
	_ = s.Create(task("a", now))
	if err := s.Create(task("a", now)); err == nil {
		t.Error("Create must not silently replace an existing task")
	}
}

func TestSecretsAreNotInTheTaskRecord(t *testing.T) {
	s, dir := store(t)
	tk := task("a", time.Now())
	tk.Env = map[string]string{"BITBUCKET_TOKEN": "s3cret", "SCAN_URL": "dx.ca"}
	if err := s.Create(tk); err != nil {
		t.Fatal(err)
	}
	record, err := os.ReadFile(filepath.Join(dir, "a", "task.json"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(record), "s3cret") || strings.Contains(string(record), "SCAN_URL") {
		t.Errorf("task.json carries the env, so any diagnostic dump leaks it:\n%s", record)
	}
	var onDisk map[string]any
	if err := json.Unmarshal(record, &onDisk); err != nil {
		t.Fatal(err)
	}
	if _, present := onDisk["env"]; present {
		t.Error("task.json still has an env field")
	}
}

func TestSecretsSurviveAReopen(t *testing.T) {
	dir := t.TempDir()
	s, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	tk := task("a", time.Now())
	tk.Env = map[string]string{"BITBUCKET_TOKEN": "s3cret"}
	if err := s.Create(tk); err != nil {
		t.Fatal(err)
	}

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	got, err := reopened.Get("a")
	if err != nil {
		t.Fatal(err)
	}
	if got.Env["BITBUCKET_TOKEN"] != "s3cret" {
		t.Error("a requeued task must still have its env after a restart")
	}
}

func TestSecretsFileIsNotWorldReadable(t *testing.T) {
	s, dir := store(t)
	tk := task("a", time.Now())
	tk.Env = map[string]string{"BITBUCKET_TOKEN": "s3cret"}
	_ = s.Create(tk)
	info, err := os.Stat(filepath.Join(dir, "a", "secrets.json"))
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode().Perm() != 0o600 {
		t.Errorf("secrets.json is %v, want 0600", info.Mode().Perm())
	}
}

func TestListIsInCreationOrder(t *testing.T) {
	s, _ := store(t)
	base := time.Now()
	for i, id := range []string{"c", "a", "b"} {
		_ = s.Create(task(id, base.Add(time.Duration(i)*time.Second)))
	}
	var ids []string
	for _, t := range s.List() {
		ids = append(ids, t.ID)
	}
	if strings.Join(ids, ",") != "c,a,b" {
		t.Errorf("List = %v, want creation order; the scheduler's fairness depends on it", ids)
	}
}

func TestReopenRebuildsTheIndexFromDisk(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.Create(task("a", time.Now()))
	_ = s.Create(task("b", time.Now().Add(time.Second)))

	reopened, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(reopened.List()) != 2 {
		t.Errorf("reopened with %d tasks, want 2", len(reopened.List()))
	}
}

func TestReopenSkipsACorruptRecordRatherThanRefusingToStart(t *testing.T) {
	dir := t.TempDir()
	s, _ := Open(dir)
	_ = s.Create(task("good", time.Now()))
	if err := os.MkdirAll(filepath.Join(dir, "bad"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bad", "task.json"), []byte("{not json"), 0o600); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(dir)
	if err != nil {
		t.Fatalf("one corrupt record must not stop the daemon starting: %v", err)
	}
	if len(reopened.List()) != 1 {
		t.Errorf("listed %d, want just the good one", len(reopened.List()))
	}
}

func TestPruneOnlyRemovesOldTerminalTasks(t *testing.T) {
	s, _ := store(t)
	old := time.Now().Add(-48 * time.Hour)
	_ = s.Create(Task{ID: "done", Status: StatusSucceeded, CreatedAt: old})
	_ = s.Create(Task{ID: "lost", Status: StatusLost, CreatedAt: old})
	_ = s.Create(Task{ID: "running", Status: StatusRunning, CreatedAt: old})
	_ = s.Create(Task{ID: "recent", Status: StatusSucceeded, CreatedAt: time.Now()})

	if got := s.Prune(24*time.Hour, time.Now()); got != 2 {
		t.Errorf("pruned %d, want 2", got)
	}
	for _, id := range []string{"running", "recent"} {
		if _, err := s.Get(id); err != nil {
			t.Errorf("%s should have survived: %v", id, err)
		}
	}
}

func TestTerminalAndPending(t *testing.T) {
	for status, terminal := range map[Status]bool{
		StatusQueued: false, StatusBlocked: false, StatusRunning: false,
		StatusSucceeded: true, StatusFailed: true, StatusCancelled: true, StatusLost: true,
	} {
		if got := (Task{Status: status}).Terminal(); got != terminal {
			t.Errorf("%s.Terminal() = %v, want %v", status, got, terminal)
		}
	}
	if !(Task{Status: StatusBlocked}).Pending() {
		t.Error("a blocked task is still pending; the scheduler must reconsider it every tick")
	}
}
