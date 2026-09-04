package image

import (
	"strings"
	"testing"
	"time"

	"github.com/effetmonstre/forge/internal/project"
)

const ttl = 14 * 24 * time.Hour

var lima = []byte("vmType: vz\nmemory: 1GiB\n")

func TestBaseNameIsStableForTheSameInputs(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	if BaseName(lima, now, ttl) != BaseName(lima, now, ttl) {
		t.Error("BaseName is not deterministic")
	}
}

func TestBaseNameChangesWhenTheTemplateChanges(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	if BaseName(lima, now, ttl) == BaseName(append(lima, '\n'), now, ttl) {
		t.Error("a changed template must invalidate the base image")
	}
}

func TestBaseNameRollsOverWithTheTTLBucket(t *testing.T) {
	now := time.Unix(1_780_000_000, 0)
	same := BaseName(lima, now.Add(time.Hour), ttl)
	later := BaseName(lima, now.Add(30*24*time.Hour), ttl)
	if BaseName(lima, now, ttl) != same {
		t.Error("an hour later must land in the same bucket")
	}
	if BaseName(lima, now, ttl) == later {
		t.Error("thirty days later must land in a new bucket, so the image expires")
	}
}

func projectWith(setup string, keys ...project.KeyFile) project.Project {
	return project.Project{SetupRaw: []byte(setup), Basekey: keys}
}

func TestProjectNameFoldsInTheBaseSetupAndEveryKeyFile(t *testing.T) {
	base := "forge-base-abc-100"
	p := projectWith("memory: 3GiB", project.KeyFile{Path: "Gemfile.lock", Contents: []byte("a")})

	name := ProjectName("dx", base, p)
	if !strings.HasPrefix(name, "forge-proj-dx-") {
		t.Errorf("name = %q, want it to carry the repo", name)
	}

	for label, other := range map[string]struct {
		base string
		p    project.Project
	}{
		"a new base image":   {"forge-base-abc-101", p},
		"a changed setup":    {base, projectWith("memory: 4GiB", p.Basekey...)},
		"a changed lockfile": {base, projectWith("memory: 3GiB", project.KeyFile{Path: "Gemfile.lock", Contents: []byte("b")})},
		"an added key file":  {base, projectWith("memory: 3GiB", p.Basekey[0], project.KeyFile{Path: ".nvmrc", Contents: []byte("v22")})},
		"a renamed key file": {base, projectWith("memory: 3GiB", project.KeyFile{Path: "yarn.lock", Contents: []byte("a")})},
	} {
		if ProjectName("dx", other.base, other.p) == name {
			t.Errorf("%s must invalidate the project image", label)
		}
	}
}

func TestProjectNameIsStableAcrossCalls(t *testing.T) {
	p := projectWith("x", project.KeyFile{Path: "a", Contents: []byte("1")})
	if ProjectName("dx", "b", p) != ProjectName("dx", "b", p) {
		t.Error("ProjectName is not deterministic")
	}
}

func TestSlugify(t *testing.T) {
	cases := map[string]string{
		"dx":                       "dx",
		"Feature/Some_Branch":      "feature-some-branch",
		"--leading-and-trailing--": "leading-and-trailing",
		strings.Repeat("a", 60):    strings.Repeat("a", 40),
	}
	for in, want := range cases {
		if got := Slugify(in); got != want {
			t.Errorf("Slugify(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRunNameCarriesTheTaskIDSoAVMIsAttributable(t *testing.T) {
	name := RunName("dx", "maintenance", "20260904T150405-abc123")
	if !strings.Contains(name, "20260904t150405-abc123") {
		t.Errorf("name = %q, want the task id in it; the orphan reaper depends on this", name)
	}
	if !Managed(name) {
		t.Errorf("%q must be recognised as forge-managed", name)
	}
}

func TestManagedRefusesToClaimForeignInstances(t *testing.T) {
	for _, name := range []string{"default", "ci-run-dx-spec-main-123", "my-vm", "forge", ""} {
		if Managed(name) {
			t.Errorf("Managed(%q) = true, want false so forge never destroys an instance it does not own", name)
		}
	}
}
