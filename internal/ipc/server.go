package ipc

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/simon-em/kranq/internal/exitcode"
	"github.com/simon-em/kranq/internal/state"
	"github.com/simon-em/kranq/internal/svc"
)

type Backend interface {
	Submit(req SubmitRequest) (state.Task, error)
	List() []state.Task
	Get(id string) (state.Task, error)
	Cancel(id string) error
	Status() Status
	LogPath(id string) string
	ArtifactsDir(id string) string
}

type Server struct {
	backend Backend
	poll    time.Duration
}

func NewServer(b Backend) *Server { return &Server{backend: b, poll: 250 * time.Millisecond} }

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", s.status)
	mux.HandleFunc("POST /v1/tasks", s.submit)
	mux.HandleFunc("GET /v1/tasks", s.list)
	mux.HandleFunc("GET /v1/tasks/{id}", s.get)
	mux.HandleFunc("GET /v1/tasks/{id}/logs", s.logs)
	mux.HandleFunc("GET /v1/tasks/{id}/artifacts", s.artifacts)
	mux.HandleFunc("POST /v1/tasks/{id}/cancel", s.cancel)
	return mux
}

func Listen(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	if conn, err := net.DialTimeout("unix", path, time.Second); err == nil {
		conn.Close()
		return nil, errors.New("a daemon is already listening on " + path)
	}
	_ = os.Remove(path)
	l, err := net.Listen("unix", path)
	if err != nil {
		return nil, err
	}
	return l, os.Chmod(path, 0o600)
}

func (s *Server) status(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, s.backend.Status())
}

func (s *Server) submit(w http.ResponseWriter, r *http.Request) {
	var req SubmitRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 8<<20)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, exitcode.Usage, "invalid request body: "+err.Error())
		return
	}
	t, err := s.backend.Submit(req)
	if err != nil {
		var e *svc.Error
		if errors.As(err, &e) {
			writeErr(w, http.StatusBadRequest, e.Code, e.Msg)
			return
		}
		writeErr(w, http.StatusInternalServerError, exitcode.InternalError, err.Error())
		return
	}
	writeJSON(w, http.StatusAccepted, t)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, TaskList{Tasks: s.backend.List()})
}

func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	t, ok := s.lookup(w, r)
	if !ok {
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (s *Server) cancel(w http.ResponseWriter, r *http.Request) {
	t, ok := s.lookup(w, r)
	if !ok {
		return
	}
	if err := s.backend.Cancel(t.ID); err != nil {
		writeErr(w, http.StatusInternalServerError, exitcode.InternalError, err.Error())
		return
	}
	updated, _ := s.backend.Get(t.ID)
	writeJSON(w, http.StatusOK, updated)
}

func (s *Server) logs(w http.ResponseWriter, r *http.Request) {
	t, ok := s.lookup(w, r)
	if !ok {
		return
	}
	follow := r.URL.Query().Get("follow") == "1"
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	flusher, _ := w.(http.Flusher)

	var offset int64
	for {
		n, err := copyFrom(s.backend.LogPath(t.ID), offset, w)
		offset += n
		if err != nil && !os.IsNotExist(err) {
			return
		}
		if flusher != nil {
			flusher.Flush()
		}
		if !follow {
			return
		}
		current, err := s.backend.Get(t.ID)
		if err == nil && current.Terminal() {
			if n, _ := copyFrom(s.backend.LogPath(t.ID), offset, w); n > 0 && flusher != nil {
				flusher.Flush()
			}
			return
		}
		select {
		case <-r.Context().Done():
			return
		case <-time.After(s.poll):
		}
	}
}

func copyFrom(path string, offset int64, w io.Writer) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, err
	}
	defer f.Close()
	if _, err := f.Seek(offset, io.SeekStart); err != nil {
		return 0, err
	}
	return io.Copy(w, f)
}

func (s *Server) artifacts(w http.ResponseWriter, r *http.Request) {
	t, ok := s.lookup(w, r)
	if !ok {
		return
	}
	dir := s.backend.ArtifactsDir(t.ID)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		writeErr(w, http.StatusNotFound, exitcode.NoSuchFile, "this task produced no artifacts")
		return
	}
	w.Header().Set("Content-Type", "application/gzip")
	gz := gzip.NewWriter(w)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()

	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || path == dir {
			return err
		}
		rel, relErr := filepath.Rel(dir, path)
		if relErr != nil {
			return relErr
		}
		info, infoErr := d.Info()
		if infoErr != nil {
			return infoErr
		}
		hdr, hdrErr := tar.FileInfoHeader(info, "")
		if hdrErr != nil {
			return hdrErr
		}
		hdr.Name = rel
		if err := tw.WriteHeader(hdr); err != nil || d.IsDir() {
			return err
		}
		src, openErr := os.Open(path)
		if openErr != nil {
			return openErr
		}
		defer src.Close()
		_, copyErr := io.Copy(tw, src)
		return copyErr
	})
}

func (s *Server) lookup(w http.ResponseWriter, r *http.Request) (state.Task, bool) {
	t, err := s.backend.Get(r.PathValue("id"))
	if err != nil {
		writeErr(w, http.StatusNotFound, exitcode.NoSuchFile, "no such task")
		return t, false
	}
	return t, true
}

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, httpCode, code int, msg string) {
	writeJSON(w, httpCode, ErrorBody{Error: msg, Code: code})
}

func Serve(ctx context.Context, l net.Listener, h http.Handler) error {
	srv := &http.Server{Handler: h}
	go func() {
		<-ctx.Done()
		shutdown, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdown)
	}()
	if err := srv.Serve(l); err != nil && !errors.Is(err, http.ErrServerClosed) {
		return err
	}
	return nil
}
