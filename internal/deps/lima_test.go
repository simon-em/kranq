package deps

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func fakeRelease(t *testing.T, body []byte, sums string) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "SHA256SUMS") {
			_, _ = w.Write([]byte(sums))
			return
		}
		_, _ = w.Write(body)
	}))
}

func tarball(t *testing.T, files map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range files {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o755, Size: int64(len(body)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(tw, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func sumOf(b []byte) string {
	s := sha256.Sum256(b)
	return hex.EncodeToString(s[:])
}

func withPinned(t *testing.T, sum string) {
	t.Helper()
	original := limaSHA256[runtime.GOARCH]
	limaSHA256[runtime.GOARCH] = sum
	t.Cleanup(func() { limaSHA256[runtime.GOARCH] = original })
}

func TestInstallVerifiesAgainstBothChecksums(t *testing.T) {
	body := tarball(t, map[string]string{"bin/limactl": "#!/bin/sh\necho fake\n"})
	sum := sumOf(body)
	withPinned(t, sum)
	srv := fakeRelease(t, body, fmt.Sprintf("%s  %s\n", sum, limaAsset[runtime.GOARCH]))
	defer srv.Close()

	l := Lima{Root: t.TempDir(), BaseURL: srv.URL, Client: srv.Client()}
	if err := l.Install(io.Discard); err != nil {
		t.Fatalf("Install: %v", err)
	}
	if !l.Installed() {
		t.Fatal("limactl is not where Installed() looks for it")
	}
	if _, err := os.Stat(l.Binary()); err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(l.Binary())
	if err != nil || info.Mode().Perm()&0o111 == 0 {
		t.Errorf("limactl is not executable: %v", info.Mode())
	}
}

func TestInstallRefusesATamperedDownload(t *testing.T) {
	body := tarball(t, map[string]string{"bin/limactl": "malicious"})
	withPinned(t, sumOf([]byte("something else entirely")))
	srv := fakeRelease(t, body, fmt.Sprintf("%s  %s\n", sumOf(body), limaAsset[runtime.GOARCH]))
	defer srv.Close()

	l := Lima{Root: t.TempDir(), BaseURL: srv.URL, Client: srv.Client()}
	err := l.Install(io.Discard)
	if err == nil {
		t.Fatal("a download that does not match the pinned checksum must be refused")
	}
	if !strings.Contains(err.Error(), "checksum mismatch") {
		t.Errorf("err = %v", err)
	}
	if l.Installed() {
		t.Error("it installed anyway")
	}
}

func TestInstallRefusesWhenTheServedSumsDisagreeWithThePin(t *testing.T) {
	body := tarball(t, map[string]string{"bin/limactl": "ok"})
	withPinned(t, sumOf(body))
	srv := fakeRelease(t, body, fmt.Sprintf("%s  %s\n", sumOf([]byte("lie")), limaAsset[runtime.GOARCH]))
	defer srv.Close()

	l := Lima{Root: t.TempDir(), BaseURL: srv.URL, Client: srv.Client()}
	err := l.Install(io.Discard)
	if err == nil {
		t.Fatal("SHA256SUMS disagreeing with the pin means the release was changed under us")
	}
	if !strings.Contains(err.Error(), "SHA256SUMS says") {
		t.Errorf("err = %v", err)
	}
}

func TestExtractRefusesAPathEscape(t *testing.T) {
	body := tarball(t, map[string]string{"../../escaped": "gotcha"})
	dir := t.TempDir()
	err := extract(body, filepath.Join(dir, "dest"))
	if err == nil || !strings.Contains(err.Error(), "escapes") {
		t.Errorf("err = %v, want a refusal", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "escaped")); err == nil {
		t.Error("the archive wrote outside its destination")
	}
}

func TestTheCurrentSymlinkIsSwappedAtomically(t *testing.T) {
	root := t.TempDir()
	body := tarball(t, map[string]string{"bin/limactl": "v1"})
	withPinned(t, sumOf(body))
	srv := fakeRelease(t, body, fmt.Sprintf("%s  %s\n", sumOf(body), limaAsset[runtime.GOARCH]))
	defer srv.Close()

	l := Lima{Root: root, BaseURL: srv.URL, Client: srv.Client()}
	if err := l.Install(io.Discard); err != nil {
		t.Fatal(err)
	}
	if err := l.Install(io.Discard); err != nil {
		t.Fatalf("installing over an existing install must work: %v", err)
	}
	target, err := os.Readlink(l.Current())
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(target) != "lima-"+LimaVersion {
		t.Errorf("current points at %q", target)
	}
}

func TestLimaIsNotOnTheGlobalPath(t *testing.T) {
	l := Lima{Root: "/Users/someone/.forge"}
	if !strings.Contains(l.Binary(), ".forge/deps/current/bin/limactl") {
		t.Errorf("Binary() = %q; lima must live under FORGE_HOME and be invoked by absolute path, "+
			"so a homebrew lima appearing or disappearing cannot break forge", l.Binary())
	}
}
