package daemon

import (
	"context"
	"errors"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/simon-em/kranq/internal/gitsrv"
	"github.com/simon-em/kranq/internal/token"
)

// The git endpoint is off unless an address is configured, and the address is
// expected to be loopback: cloudflared dials out to reach it, so nothing here
// ever binds a public interface.
func (d *Daemon) serveGit(ctx context.Context) {
	if d.cfg.HTTPAddr == "" {
		return
	}
	backend, err := gitsrv.Backend()
	if err != nil {
		log.Printf("git endpoint disabled: %v", err)
		return
	}
	self, err := os.Executable()
	if err != nil {
		log.Printf("git endpoint disabled: %v", err)
		return
	}

	store := &gitsrv.Store{
		Root:       d.cfg.ReposDir(),
		KranqBin:   self,
		SocketPath: d.cfg.SocketPath(),
	}
	srv := &gitsrv.Server{
		Store:   store,
		Backend: backend,
		Tokens:  func() (*token.Set, error) { return token.Load(token.Path(d.cfg.Home)) },
		OnAuth:  func(name, repo string) { d.touchToken(name) },
		Log:     log.Printf,
	}

	listener, err := net.Listen("tcp", d.cfg.HTTPAddr)
	if err != nil {
		log.Printf("git endpoint disabled: %v", err)
		return
	}
	mux := http.NewServeMux()
	mux.Handle("/git/", http.StripPrefix("/git/", srv.Handler()))
	mux.HandleFunc("/health", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("ok\n"))
	})

	// A push of a large tree is slow and a followed run is slower, so neither
	// gets a write deadline. The read header timeout still closes a connection
	// that never sends a request.
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 20 * time.Second}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = server.Shutdown(shutdown)
	}()
	log.Printf("git endpoint on http://%s/git/<repo>.git", listener.Addr())
	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			log.Printf("git endpoint stopped: %v", err)
		}
	}()
}

func (d *Daemon) touchToken(name string) {
	path := token.Path(d.cfg.Home)
	set, err := token.Load(path)
	if err != nil {
		return
	}
	set.Touch(name, time.Now())
	_ = set.Save(path)
}

func (c Config) ReposDir() string { return filepath.Join(c.Home, "repos") }
