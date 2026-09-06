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
	"sync"
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
	l := Lima{Root: "/Users/someone/.kranq"}
	if !strings.Contains(l.Binary(), ".kranq/deps/current/bin/limactl") {
		t.Errorf("Binary() = %q; lima must live under KRANQ_HOME and be invoked by absolute path, "+
			"so a homebrew lima appearing or disappearing cannot break kranq", l.Binary())
	}
}

// The asset table is keyed by architecture and every entry in it is a macOS
// build, so without a GOOS check a linux client would download a Darwin tarball
// and report success.
func TestSupportedIsFalseOffDarwin(t *testing.T) {
	if runtime.GOOS == "darwin" {
		if !Supported() {
			t.Fatalf("lima should be supported on %s/%s", runtime.GOOS, runtime.GOARCH)
		}
		return
	}
	if Supported() {
		t.Fatalf("%s/%s reported as supported, but every asset is a macOS build",
			runtime.GOOS, runtime.GOARCH)
	}
}

func TestInstallRefusesAnUnsupportedPlatform(t *testing.T) {
	if Supported() {
		t.Skip("this machine is supported")
	}
	if err := (Lima{Root: t.TempDir()}).Install(io.Discard); err == nil {
		t.Fatal("an unsupported platform was installed onto")
	}
}

func TestEnsureInstalledIsANoOpWhenItIsAlreadyThere(t *testing.T) {
	root := t.TempDir()
	lima := Lima{Root: root}
	// Stand in for an installed copy: the binary at the path Installed() checks.
	bin := filepath.Join(lima.Dir(), "bin")
	if err := os.MkdirAll(bin, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "limactl"), []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := link(lima.Dir(), lima.Current()); err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	path, err := lima.EnsureInstalled(&out)
	if err != nil {
		t.Fatal(err)
	}
	if path != lima.Binary() {
		t.Fatalf("got %q", path)
	}
	if out.Len() != 0 {
		t.Fatalf("it said something while doing nothing: %q", out.String())
	}
}

// Two commands starting at once must not both download it.
func TestConcurrentEnsureInstallsOnce(t *testing.T) {
	root := t.TempDir()
	if !Supported() {
		t.Skip("no lima build for this platform")
	}
	body := tarball(t, map[string]string{"bin/limactl": "#!/bin/sh\n"})
	sum := sumOf(body)
	withPinned(t, sum)
	server := fakeRelease(t, body, fmt.Sprintf("%s  %s\n", sum, limaAsset[runtime.GOARCH]))
	defer server.Close()

	var mu sync.Mutex
	downloads := 0
	var wg sync.WaitGroup
	for range 4 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var out strings.Builder
			lima := Lima{Root: root, BaseURL: server.URL}
			if _, err := lima.EnsureInstalled(&out); err != nil {
				t.Error(err)
				return
			}
			if strings.Contains(out.String(), "downloading") {
				mu.Lock()
				downloads++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	if downloads != 1 {
		t.Fatalf("%d of 4 concurrent callers downloaded it; the lock is not holding", downloads)
	}
}
