package project

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/simon-em/kranq/internal/task"
)

const (
	DefaultFile    = "Kranqfile"
	DefaultWorkdir = "/kranq/build"
	CopyStage      = "/tmp/kranq-stage"
)

type Build struct {
	Memory string
	CPUs   int
	Disk   string
	Layers []Layer
}

type EnvVar struct {
	Name  string
	Value string
}

type Copy struct {
	Sources []string
	Dest    string
	Workdir string
}

// Layer is what one RUN and everything staged for it becomes. WORKDIR and ENV
// carry forward from earlier instructions, exactly as they do in a Dockerfile.
type Layer struct {
	Copies  []Copy
	Workdir string
	Env     []EnvVar
	Run     string
	Line    int
}

// File carries a digest and a host path rather than the bytes, so copying a
// directory of any size costs one open at the moment it is uploaded.
type File struct {
	Dest   string
	Source string
	Mode   fs.FileMode
	Digest string
}

type Resolved struct {
	Layer
	Files []File
	Roots []string
}

type Project struct {
	Build  Build
	Layers []Resolved
}

func (p Project) MemoryGiB() (float64, bool) { return gib(p.Build.Memory) }

func (p Project) DiskGiB() (float64, bool) { return gib(p.Build.Disk) }

func (l Layer) Dir() string {
	if l.Workdir != "" {
		return l.Workdir
	}
	return DefaultWorkdir
}

// Summary is the layer as a human recognises it in a log: the instruction that
// made it, shortened. It is not part of the layer's identity.
func (l Layer) Summary() string {
	if l.Run != "" {
		return "RUN " + shorten(firstLine(l.Run))
	}
	var names []string
	for _, c := range l.Copies {
		names = append(names, c.Sources...)
	}
	return "COPY " + shorten(strings.Join(names, " "))
}

func firstLine(s string) string {
	line, _, _ := strings.Cut(strings.TrimSpace(s), "\n")
	return line
}

func shorten(s string) string {
	if len(s) <= 44 {
		return s
	}
	return s[:41] + "..."
}

func Load(checkout, file string) (Project, error) {
	if file == "" {
		file = DefaultFile
	}
	var p Project
	raw, err := os.ReadFile(filepath.Join(checkout, file))
	if err != nil {
		if os.IsNotExist(err) {
			return p, fmt.Errorf("%s not found in the checkout", file)
		}
		return p, err
	}
	p.Build, err = parse(string(raw), file)
	if err != nil {
		return p, err
	}
	if err := p.Build.validate(file); err != nil {
		return p, err
	}
	p.Layers, err = resolve(checkout, p.Build.Layers)
	if err != nil {
		return p, fmt.Errorf("%s: %w", file, err)
	}
	return p, nil
}

func (b Build) validate(file string) error {
	for _, pair := range []struct{ field, value string }{{"MEMORY", b.Memory}, {"DISK", b.Disk}} {
		if pair.value == "" {
			continue
		}
		if _, err := task.ParseMemory(pair.value); err != nil {
			return fmt.Errorf("%s: %s: %w", file, pair.field, err)
		}
	}
	return nil
}

func resolve(checkout string, layers []Layer) ([]Resolved, error) {
	out := make([]Resolved, 0, len(layers))
	for _, l := range layers {
		files, roots, err := resolveCopies(checkout, l)
		if err != nil {
			return nil, fmt.Errorf("line %d: %w", l.Line, err)
		}
		out = append(out, Resolved{Layer: l, Files: files, Roots: roots})
	}
	return out, nil
}

// resolveCopies turns each COPY into the exact guest paths it produces, so the
// layer's identity is the contents that land in the image and not the patterns
// that happened to select them.
func resolveCopies(checkout string, l Layer) ([]File, []string, error) {
	seen := map[string]File{}
	roots := map[string]bool{}
	for _, c := range l.Copies {
		matched, err := expand(checkout, c.Sources)
		if err != nil {
			return nil, nil, err
		}
		dest := guestPath(c)
		roots[dest] = true
		for _, m := range matched {
			if err := collect(m, dest, intoDir(c, matched), seen); err != nil {
				return nil, nil, err
			}
		}
	}
	files := make([]File, 0, len(seen))
	for _, f := range seen {
		files = append(files, f)
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Dest < files[j].Dest })
	return files, sorted(roots), nil
}

// Docker's rule: the destination is a directory when it is written as one, or
// when more than one source has to fit in it. A directory source always spills
// its contents into the destination, whatever the destination looks like.
func intoDir(c Copy, matched []string) bool {
	return len(matched) > 1 || strings.HasSuffix(c.Dest, "/") ||
		c.Dest == "." || c.Dest == "./" || c.Dest == ".."
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for k := range set {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func guestPath(c Copy) string {
	workdir := c.Workdir
	if workdir == "" {
		workdir = DefaultWorkdir
	}
	if path.IsAbs(c.Dest) {
		return path.Clean(c.Dest)
	}
	return path.Join(workdir, c.Dest)
}

func expand(checkout string, sources []string) ([]string, error) {
	var out []string
	for _, src := range sources {
		if err := safe(src); err != nil {
			return nil, err
		}
		matches, err := filepath.Glob(filepath.Join(checkout, src))
		if err != nil {
			return nil, fmt.Errorf("COPY: %q is not a valid pattern: %w", src, err)
		}
		if len(matches) == 0 {
			return nil, fmt.Errorf("COPY: %q matched nothing in the checkout", src)
		}
		sort.Strings(matches)
		out = append(out, matches...)
	}
	return out, nil
}

func safe(pattern string) error {
	if path.IsAbs(pattern) || filepath.IsAbs(pattern) {
		return fmt.Errorf("COPY: source %q must be relative to the checkout", pattern)
	}
	for _, part := range strings.Split(filepath.ToSlash(pattern), "/") {
		if part == ".." {
			return fmt.Errorf("COPY: source %q reaches outside the checkout", pattern)
		}
	}
	return nil
}

// A .git directory is skipped because its packfiles differ between two clones
// of the same commit, which would give every machine a different layer for
// identical source.
func collect(hostPath, dest string, intoDir bool, into map[string]File) error {
	info, err := os.Lstat(hostPath)
	if err != nil {
		return fmt.Errorf("COPY: %w", err)
	}
	if info.IsDir() {
		return filepath.WalkDir(hostPath, func(p string, d fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			if d.IsDir() {
				if d.Name() == ".git" {
					return filepath.SkipDir
				}
				return nil
			}
			rel, relErr := filepath.Rel(hostPath, p)
			if relErr != nil {
				return relErr
			}
			return collect(p, path.Join(dest, filepath.ToSlash(rel)), false, into)
		})
	}
	if !info.Mode().IsRegular() {
		return fmt.Errorf("COPY: %s is not a regular file", filepath.Base(hostPath))
	}
	digest, err := digestOf(hostPath)
	if err != nil {
		return fmt.Errorf("COPY: %w", err)
	}
	if intoDir {
		dest = path.Join(dest, filepath.Base(hostPath))
	}
	into[dest] = File{Dest: dest, Source: hostPath, Mode: normalMode(info.Mode()), Digest: digest}
	return nil
}

// Only the executable bit survives, because it is the only permission git
// records; anything else would make a layer depend on the builder's umask.
func normalMode(m fs.FileMode) fs.FileMode {
	if m&0o111 != 0 {
		return 0o755
	}
	return 0o644
}

func digestOf(hostPath string) (string, error) {
	f, err := os.Open(hostPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func gib(v string) (float64, bool) {
	if v == "" {
		return 0, false
	}
	n, err := task.ParseMemory(v)
	if err != nil {
		return 0, false
	}
	return float64(n) / (1 << 30), true
}
