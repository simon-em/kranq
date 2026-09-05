package deps

import (
	"io"
	"os"
	"path/filepath"
	"syscall"
)

// Two commands starting at once would otherwise both download lima and both
// swap the symlink. The kernel releases a flock when the process dies, so there
// is no stale-lock case to recover from.
type lock struct{ f *os.File }

func acquire(root string) (*lock, error) {
	if err := os.MkdirAll(filepath.Join(root, "deps"), 0o700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(filepath.Join(root, "deps", ".lock"), os.O_CREATE|os.O_RDWR, 0o600)
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

// EnsureInstalled installs lima if it is not there, and does nothing if it is.
// The check is repeated under the lock, so of two commands racing only one
// downloads and the other finds the work already done.
func (l Lima) EnsureInstalled(out io.Writer) (string, error) {
	if l.Installed() {
		return l.Binary(), nil
	}
	if !Supported() {
		return "", errUnsupported()
	}
	guard, err := acquire(l.Root)
	if err != nil {
		return "", err
	}
	defer guard.release()

	if l.Installed() {
		return l.Binary(), nil
	}
	if err := l.Install(out); err != nil {
		return "", err
	}
	return l.Binary(), nil
}
