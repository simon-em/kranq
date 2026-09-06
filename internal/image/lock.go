package image

import (
	"os"
	"path/filepath"
	"syscall"
)

// Jobs run in separate `forge exec` processes, so an in-process mutex would let
// two of them build the same layer at once. The kernel releases a flock when
// the process dies, so there is no stale-lock case to recover from.
type lock struct{ f *os.File }

func (s metaStore) acquire(name string) (*lock, error) {
	if err := os.MkdirAll(s.dir, 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(s.dir, name+".lock"), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return &lock{f: f}, nil
}

func (l *lock) release() {
	syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	l.f.Close()
}
