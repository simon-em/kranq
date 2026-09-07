package image

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/simon-em/kranq/internal/project"
)

const (
	BasePrefix  = "kranq-base"
	LayerPrefix = "kranq-layer"
	RunPrefix   = "kranq-run"
)

// Names from before this tool was renamed, and from the single-layer scheme the
// Kranqfile replaced. Nothing creates them any more. They are still recognised
// so prune can sweep them: an image this tool built but no longer answers to
// would sit on a build machine forever, and Managed is what permits deleting
// anything at all.
var legacyPrefixes = []string{
	"kranq-proj-",
	"forge-base-", "forge-layer-", "forge-run-", "forge-proj-",
}

func Legacy(name string) bool {
	for _, p := range legacyPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

var notSlug = regexp.MustCompile(`[^a-z0-9]+`)

func Slugify(s string) string {
	s = notSlug.ReplaceAllString(strings.ToLower(s), "-")
	s = strings.Trim(s, "-")
	if len(s) > 40 {
		s = strings.Trim(s[:40], "-")
	}
	return s
}

func BaseName(limaYAML []byte, now time.Time, ttl time.Duration) string {
	sum := sha256.Sum256(limaYAML)
	bucket := now.Unix() / int64(ttl/time.Second)
	return fmt.Sprintf("%s-%x-%d", BasePrefix, sum[:5], bucket)
}

// LayerName is a function of the parent and of what the layer does, and of
// nothing else. No repository, no branch, no label: two projects that install
// the same packages against the same lockfile get the same layer and build it
// once between them.
func LayerName(parent string, l project.Resolved, depth int) string {
	h := sha256.New()
	fmt.Fprintf(h, "parent\x00%s\x00", parent)
	fmt.Fprintf(h, "workdir\x00%s\x00", l.Dir())
	for _, e := range l.Env {
		fmt.Fprintf(h, "env\x00%s\x00%s\x00", e.Name, e.Value)
	}
	// An arg's value is part of what the layer is: two builds that differ by
	// one are two layers, which is what makes ARG safe to have at all.
	for _, a := range l.Values {
		fmt.Fprintf(h, "arg\x00%s\x00%s\x00", a.Name, a.Value)
	}
	// A secret's *name* only. The value is not what makes the layer different
	// -- the same gems come back whoever fetched them -- and hashing it would
	// rebuild every layer the day the credential is rotated. The name still
	// counts, because a RUN that can suddenly see one may do something else.
	for _, name := range l.Secrets {
		fmt.Fprintf(h, "secret\x00%s\x00", name)
	}
	fmt.Fprintf(h, "run\x00%s\x00", l.Run)
	for _, f := range l.Files {
		fmt.Fprintf(h, "file\x00%s\x00%o\x00%s\x00", f.Dest, f.Mode, f.Digest)
	}
	return fmt.Sprintf("%s-%02d-%x", LayerPrefix, depth, h.Sum(nil)[:6])
}

// Root is what the first layer is keyed on: the base image, plus any request
// that a later clone cannot undo. Disk is the only one, because lima can grow a
// disk when cloning but never shrink it, so two projects asking for different
// sizes must not share a chain. Memory and cpus are not here: the job's own
// clone overrides them, so a shared layer is not sized by whoever built it.
func Root(base, disk string) string {
	if disk == "" {
		return base
	}
	return base + "\x00disk=" + disk
}

// Chain is every image name the build passes through, base first. Each entry
// depends on the one before it, so a change at index i leaves 0..i-1 valid and
// rebuilds only the tail.
func Chain(base, disk string, layers []project.Resolved) []string {
	names := make([]string, 0, len(layers)+1)
	names = append(names, base)
	parent := Root(base, disk)
	for i, l := range layers {
		parent = LayerName(parent, l, i+1)
		names = append(names, parent)
	}
	return names
}

const MaxRunName = 40

// A task need not name a repository, so the parts are joined rather than
// formatted: an empty one used to leave a doubled hyphen, and lima refuses the
// instance outright -- "kranq-run--t-6a331d1b is not a valid identifier", after
// the image had already been built.
func RunName(repo, label, taskID string) string {
	sum := sha256.Sum256([]byte(taskID))
	suffix := fmt.Sprintf("-%x", sum[:4])
	parts := []string{RunPrefix}
	for _, part := range []string{Slugify(repo), Slugify(label)} {
		if part != "" {
			parts = append(parts, part)
		}
	}
	base := strings.Join(parts, "-")
	if room := MaxRunName - len(suffix); len(base) > room {
		base = strings.Trim(base[:room], "-")
	}
	return base + suffix
}

func Managed(name string) bool {
	for _, p := range []string{BasePrefix + "-", LayerPrefix + "-", RunPrefix + "-"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return Legacy(name)
}

func IsLayer(name string) bool { return strings.HasPrefix(name, LayerPrefix+"-") }

func IsBase(name string) bool { return strings.HasPrefix(name, BasePrefix+"-") }
