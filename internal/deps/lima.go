package deps

import (
	"archive/tar"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

const (
	LimaVersion = "2.2.0"
	limaBaseURL = "https://github.com/lima-vm/lima/releases/download/v" + LimaVersion
)

var limaSHA256 = map[string]string{
	"arm64": "bbdef91774885a0d05f7b048c4eb89ae2bcf3a0c252ae7ca7934e63df76d93c3",
	"amd64": "0d6f99c19f6e4bc3c92730c4c29d929e6927f0cb0a0ba1a84383367135a8ff31",
}

var limaAsset = map[string]string{
	"arm64": "lima-" + LimaVersion + "-Darwin-arm64.tar.gz",
	"amd64": "lima-" + LimaVersion + "-Darwin-x86_64.tar.gz",
}

type Lima struct {
	Root    string
	Client  *http.Client
	BaseURL string
}

func (l Lima) client() *http.Client {
	if l.Client != nil {
		return l.Client
	}
	return &http.Client{Timeout: 10 * time.Minute}
}

func (l Lima) baseURL() string {
	if l.BaseURL != "" {
		return l.BaseURL
	}
	return limaBaseURL
}

func (l Lima) Dir() string     { return filepath.Join(l.Root, "deps", "lima-"+LimaVersion) }
func (l Lima) Current() string { return filepath.Join(l.Root, "deps", "current") }
func (l Lima) Binary() string  { return filepath.Join(l.Current(), "bin", "limactl") }

func (l Lima) Installed() bool {
	info, err := os.Stat(l.Binary())
	return err == nil && !info.IsDir()
}

// Supported says whether this machine can run lima at all. The asset table is
// keyed by architecture and every entry in it is a macOS build, so without the
// GOOS check a linux client would happily download a Darwin tarball and call it
// installed.
func Supported() bool {
	if runtime.GOOS != "darwin" {
		return false
	}
	_, ok := limaAsset[runtime.GOARCH]
	return ok
}

func errUnsupported() error {
	return fmt.Errorf("no lima build for %s/%s", runtime.GOOS, runtime.GOARCH)
}

func (l Lima) Install(out io.Writer) error {
	if !Supported() {
		return errUnsupported()
	}
	asset := limaAsset[runtime.GOARCH]
	want := limaSHA256[runtime.GOARCH]

	// Created here and not left to MkdirAll's parent creation, which would give
	// the kranq home 0755 and undermine the socket's only access control.
	if err := os.MkdirAll(filepath.Join(l.Root, "deps"), 0o700); err != nil {
		return err
	}

	fmt.Fprintf(out, "downloading %s\n", asset)
	body, err := l.fetch(l.baseURL() + "/" + asset)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(body)
	got := hex.EncodeToString(sum[:])
	if got != want {
		return fmt.Errorf("checksum mismatch for %s:\n  expected %s (pinned in this binary)\n  got      %s",
			asset, want, got)
	}

	published, err := l.publishedSum(asset)
	if err != nil {
		return err
	}
	if published != want {
		return fmt.Errorf("SHA256SUMS says %s for %s but this binary pins %s; refusing to install",
			published, asset, want)
	}
	fmt.Fprintf(out, "verified %s against both the pinned checksum and the published SHA256SUMS\n", got[:16])

	if err := os.RemoveAll(l.Dir()); err != nil {
		return err
	}
	if err := extract(body, l.Dir()); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Join(l.Dir(), "bin", "limactl"), 0o755); err != nil {
		return err
	}
	return link(l.Dir(), l.Current())
}

func (l Lima) fetch(url string) ([]byte, error) {
	resp, err := l.client().Get(url)
	if err != nil {
		return nil, fmt.Errorf("fetching %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetching %s: %s", url, resp.Status)
	}
	return io.ReadAll(resp.Body)
}

func (l Lima) publishedSum(asset string) (string, error) {
	body, err := l.fetch(l.baseURL() + "/SHA256SUMS")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == asset {
			return fields[0], nil
		}
	}
	return "", fmt.Errorf("SHA256SUMS does not mention %s", asset)
}

func extract(archive []byte, dest string) error {
	gz, err := gzip.NewReader(strings.NewReader(string(archive)))
	if err != nil {
		return err
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
		clean := filepath.Clean(hdr.Name)
		if strings.HasPrefix(clean, "..") || filepath.IsAbs(clean) {
			return fmt.Errorf("archive entry escapes the destination: %q", hdr.Name)
		}
		target := filepath.Join(dest, clean)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(target, 0o755); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			_ = os.Remove(target)
			if err := os.Symlink(hdr.Linkname, target); err != nil {
				return err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
				return err
			}
			f, err := os.OpenFile(target, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, os.FileMode(hdr.Mode)&0o777)
			if err != nil {
				return err
			}
			if _, err := io.Copy(f, tr); err != nil {
				f.Close()
				return err
			}
			f.Close()
		}
	}
}

func link(target, name string) error {
	tmp := name + ".new"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}
