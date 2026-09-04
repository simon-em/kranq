package image

import (
	"crypto/sha256"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/effetmonstre/forge/internal/project"
)

const (
	BasePrefix    = "forge-base"
	ProjectPrefix = "forge-proj"
	RunPrefix     = "forge-run"
)

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

func ProjectName(repo, baseName string, p project.Project) string {
	h := sha256.New()
	fmt.Fprintln(h, baseName)
	h.Write(p.SetupRaw)
	for _, f := range p.Basekey {
		h.Write([]byte(f.Path))
		h.Write(f.Contents)
	}
	return fmt.Sprintf("%s-%s-%x", ProjectPrefix, Slugify(repo), h.Sum(nil)[:5])
}

const MaxRunName = 40

func RunName(repo, label, taskID string) string {
	sum := sha256.Sum256([]byte(taskID))
	suffix := fmt.Sprintf("-%x", sum[:4])
	base := fmt.Sprintf("%s-%s-%s", RunPrefix, Slugify(repo), Slugify(label))
	if room := MaxRunName - len(suffix); len(base) > room {
		base = strings.Trim(base[:room], "-")
	}
	return base + suffix
}

func Managed(name string) bool {
	for _, p := range []string{BasePrefix + "-", ProjectPrefix + "-", RunPrefix + "-"} {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}
