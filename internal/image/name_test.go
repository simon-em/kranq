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

func TestAVMIsAttributableToItsTask(t *testing.T) {
	const id = "20260904T150405-abc123"
	name := RunName("dx", "maintenance", id)

	if RunName("dx", "maintenance", id) != name {
		t.Fatal("RunName is not deterministic, so a VM cannot be traced back to its task")
	}
	if RunName("dx", "maintenance", "20260904T150405-different") == name {
		t.Error("two tasks share a name, so the reaper cannot tell whose VM it is")
	}
	if !Managed(name) {
		t.Errorf("%q must be recognised as forge-managed", name)
	}
	if !strings.Contains(name, "dx") || !strings.Contains(name, "maintenance") {
		t.Errorf("name = %q, want the repo and label legible to a human running limactl list", name)
	}
}

func TestManagedRefusesToClaimForeignInstances(t *testing.T) {
	for _, name := range []string{"default", "ci-run-dx-spec-main-123", "my-vm", "forge", ""} {
		if Managed(name) {
			t.Errorf("Managed(%q) = true, want false so forge never destroys an instance it does not own", name)
		}
	}
}

func TestRunNameStaysShortEnoughForLimaToBuildItsSocketPath(t *testing.T) {
	long := RunName("some-quite-long-repository-name", "an-equally-long-task-label",
		"20260904T203017-b95c23b6c70256e8")
	if len(long) > MaxRunName {
		t.Fatalf("name is %d chars: %q", len(long), long)
	}

	home := "/Users/averyverylongusername/.lima"
	path := home + "/" + long + "/ssh.sock.1234567890123456"
	if len(path) >= 104 {
		t.Errorf("lima would build a %d byte socket path from this name, and macOS caps it at 104:\n%s",
			len(path), path)
	}
	if !Managed(long) {
		t.Errorf("%q must still be recognised as forge-managed after truncation", long)
	}
	if strings.HasSuffix(strings.TrimSuffix(long, long[len(long)-9:]), "-") {
		t.Errorf("truncation left a dangling separator: %q", long)
	}
}

func TestRunNameIsUniquePerTaskEvenWhenTruncated(t *testing.T) {
	repo, label := "some-quite-long-repository-name", "an-equally-long-task-label"
	a := RunName(repo, label, "20260904T203017-aaaaaaaaaaaaaaaa")
	b := RunName(repo, label, "20260904T203017-bbbbbbbbbbbbbbbb")
	if a == b {
		t.Errorf("two tasks collided on %q, so one would destroy the other's VM", a)
	}
}

func TestRunNameIsStableForOneTask(t *testing.T) {
	id := "20260904T203017-b95c23b6c70256e8"
	if RunName("dx", "spec", id) != RunName("dx", "spec", id) {
		t.Error("RunName must be deterministic; the reaper looks VMs up by it")
	}
}
