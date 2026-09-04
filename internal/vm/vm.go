package vm

import (
	"context"
	"io"
)

type Instance struct {
	Name   string `json:"name"`
	Status string `json:"status"`
	Dir    string `json:"dir"`
	Arch   string `json:"arch"`
	CPUs   int    `json:"cpus"`
	Memory int64  `json:"memory"`
}

func (i Instance) Running() bool { return i.Status == "Running" }

type Resources struct {
	MemoryGiB float64
	CPUs      int
	DiskGiB   float64
}

type Driver interface {
	List(ctx context.Context) ([]Instance, error)
	Exists(name string) bool
	CreateFromTemplate(ctx context.Context, name string, template []byte, out io.Writer) error
	Clone(ctx context.Context, src, dst string, r Resources) error
	Start(ctx context.Context, name string) error
	Stop(ctx context.Context, name string, force bool) error
	Delete(ctx context.Context, name string) error
	CopyIn(ctx context.Context, name, hostPath, guestPath string, recursive bool) error
	CopyOut(ctx context.Context, name, guestPath, hostPath string, recursive bool) error
	Shell(ctx context.Context, name, script string, out io.Writer) (int, error)
}
