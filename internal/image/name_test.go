package image

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/simon-em/kranq/internal/project"
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

func layer(run string, files ...project.File) project.Resolved {
	return project.Resolved{Layer: project.Layer{Run: run}, Files: files}
}

func file(dest, digest string) project.File {
	return project.File{Dest: dest, Digest: digest, Mode: 0o644}
}

func TestALayerIsKeyedOnItsParentAndItsContent(t *testing.T) {
	base := "kranq-base-abc-100"
	l := layer("bundle install", file("Gemfile.lock", "aaa"))
	name := LayerName(base, l, 1)

	if !strings.HasPrefix(name, LayerPrefix+"-01-") {
		t.Errorf("name = %q, want the prefix and the depth", name)
	}
	if LayerName(base, l, 1) != name {
		t.Error("LayerName is not deterministic")
	}

	env := project.Resolved{Layer: project.Layer{Run: "bundle install", Env: []project.EnvVar{{Name: "A", Value: "1"}}}, Files: l.Files}
	workdir := project.Resolved{Layer: project.Layer{Run: "bundle install", Workdir: "/elsewhere"}, Files: l.Files}
	for label, other := range map[string]struct {
		parent string
		l      project.Resolved
		depth  int
	}{
		"a new base image":   {"kranq-base-abc-101", l, 1},
		"a changed command":  {base, layer("bundle install --jobs 4", l.Files...), 1},
		"a changed lockfile": {base, layer("bundle install", file("Gemfile.lock", "bbb")), 1},
		"a renamed file":     {base, layer("bundle install", file("yarn.lock", "aaa")), 1},
		"an added file":      {base, layer("bundle install", l.Files[0], file(".nvmrc", "ccc")), 1},
		"a changed mode":     {base, layer("bundle install", project.File{Dest: "Gemfile.lock", Digest: "aaa", Mode: 0o755}), 1},
		"a changed env":      {base, env, 1},
		"a changed workdir":  {base, workdir, 1},
	} {
		if LayerName(other.parent, other.l, other.depth) == name {
			t.Errorf("%s must invalidate the layer", label)
		}
	}
}

// The whole point of content addressing: the same work under a different
// repository is the same layer, built once and shared.
func TestALayerDoesNotDependOnWhoBuiltIt(t *testing.T) {
	base := "kranq-base-abc-100"
	l := layer("apt-get install -y default-jdk")
	if LayerName(base, l, 1) != LayerName(base, l, 1) {
		t.Fatal("LayerName is not deterministic")
	}
	if strings.Contains(LayerName(base, l, 1), "dx") {
		t.Error("a layer name carries a repository, so two projects cannot share it")
	}
}

func TestAChainInvalidatesOnlyTheTail(t *testing.T) {
	base := "kranq-base-abc-100"
	layers := []project.Resolved{
		layer("apt-get install -y default-jdk"),
		layer("ruby-build 3.4.1 /opt/ci/ruby", file(".ruby-version", "aaa")),
		layer("bundle install", file("Gemfile.lock", "bbb")),
	}
	before := Chain(base, "", layers)
	if len(before) != 4 || before[0] != base {
		t.Fatalf("chain = %v, want the base plus one name per layer", before)
	}

	layers[2] = layer("bundle install", file("Gemfile.lock", "changed"))
	after := Chain(base, "", layers)
	for i := 0; i < 3; i++ {
		if before[i] != after[i] {
			t.Errorf("layer %d changed, so a lockfile edit rebuilt work it did not touch", i)
		}
	}
	if before[3] == after[3] {
		t.Error("the edited layer was reused")
	}
}

// Lima can grow a disk when cloning but never shrink one, so two projects
// asking for different sizes must not land on the same layer.
func TestADifferentDiskIsADifferentChain(t *testing.T) {
	base := "kranq-base-abc-100"
	layers := []project.Resolved{layer("apt-get install -y default-jdk")}
	if Chain(base, "", layers)[1] == Chain(base, "80GiB", layers)[1] {
		t.Error("a bigger disk reused a chain built on a smaller one")
	}
	if Chain(base, "80GiB", layers)[1] != Chain(base, "80GiB", layers)[1] {
		t.Error("Chain is not deterministic")
	}
	if Chain(base, "80GiB", layers)[0] != base {
		t.Error("the first layer must still be cloned from the base itself")
	}
}

func TestALayerNameFitsLimasSocketPath(t *testing.T) {
	name := LayerName("kranq-base-abcdef0123-4567", layer("x"), 99)
	path := "/Users/averyverylongusername/.lima/" + name + "/ssh.sock.1234567890123456"
	if len(path) >= 104 {
		t.Errorf("lima would build a %d byte socket path from %q, and macOS caps it at 104", len(path), name)
	}
	if !Managed(name) || !IsLayer(name) {
		t.Errorf("%q must be recognised as a kranq layer", name)
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
		t.Errorf("%q must be recognised as kranq-managed", name)
	}
	if !strings.Contains(name, "dx") || !strings.Contains(name, "maintenance") {
		t.Errorf("name = %q, want the repo and label legible to a human running limactl list", name)
	}
}

func TestManagedRefusesToClaimForeignInstances(t *testing.T) {
	for _, name := range []string{"default", "ci-run-dx-spec-main-123", "my-vm", "kranq", ""} {
		if Managed(name) {
			t.Errorf("Managed(%q) = true, want false so kranq never destroys an instance it does not own", name)
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
		t.Errorf("%q must still be recognised as kranq-managed after truncation", long)
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

// A task that named no repository left a doubled hyphen in the instance name,
// and lima refuses that: "kranq-run--t-6a331d1b is not a valid identifier".
// It surfaced after the image was built, which is the expensive place to fail.
func TestARunNameSurvivesAMissingPart(t *testing.T) {
	valid := regexp.MustCompile(`^[A-Za-z0-9]+(?:[._-][A-Za-z0-9]+)*$`)
	for _, c := range []struct{ repo, label string }{
		{"", "t"},
		{"dx", ""},
		{"", ""},
		{"dx", "spec"},
	} {
		got := RunName(c.repo, c.label, "20260907T183908-0b9f29f259bc11ad")
		if !valid.MatchString(got) {
			t.Errorf("RunName(%q, %q) = %q, which lima refuses", c.repo, c.label, got)
		}
		if len(got) > MaxRunName {
			t.Errorf("RunName(%q, %q) = %q, longer than %d", c.repo, c.label, got, MaxRunName)
		}
	}
}

// An arg's value makes the layer, so two builds that differ by one are two
// layers rather than a collision -- which is the objection that kept ARG out.
func TestAnArgValueChangesTheLayerName(t *testing.T) {
	base := project.Resolved{Layer: project.Layer{Run: "bundle install"}}
	with := func(name, value string) string {
		l := base
		l.Values = []project.EnvVar{{Name: name, Value: value}}
		return LayerName("parent", l, 1)
	}
	if with("RUBY", "3.4.1") == with("RUBY", "3.5.0") {
		t.Error("two arg values produced one layer; the builds would collide")
	}
	if with("RUBY", "3.4.1") == LayerName("parent", base, 1) {
		t.Error("declaring an arg did not change the layer at all")
	}
	if with("RUBY", "3.4.1") != with("RUBY", "3.4.1") {
		t.Error("the same arg produced two layers")
	}
}

// A secret's value must not. The same gems come back whoever fetched them, and
// hashing the credential would rebuild every layer the day it is rotated --
// which is the reason a secret is not just an arg.
func TestASecretValueDoesNotChangeTheLayerName(t *testing.T) {
	l := project.Resolved{Layer: project.Layer{Run: "bundle install", Secrets: []string{"TOKEN"}}}
	before := l
	before.Private = []project.EnvVar{{Name: "TOKEN", Value: "old-token"}}
	after := l
	after.Private = []project.EnvVar{{Name: "TOKEN", Value: "rotated-token"}}

	if LayerName("parent", before, 1) != LayerName("parent", after, 1) {
		t.Error("rotating a secret renamed the layer, so every image would rebuild")
	}
	// The name still counts: a RUN that can suddenly see a token may do
	// something else, and that is a different layer.
	bare := project.Resolved{Layer: project.Layer{Run: "bundle install"}}
	if LayerName("parent", before, 1) == LayerName("parent", bare, 1) {
		t.Error("declaring a secret did not change the layer")
	}
}

// The value must not reach the name by any route, including as a substring.
func TestASecretValueIsNowhereInTheName(t *testing.T) {
	l := project.Resolved{Layer: project.Layer{Run: "true", Secrets: []string{"TOKEN"}}}
	l.Private = []project.EnvVar{{Name: "TOKEN", Value: "deadbeefdeadbeef"}}
	if strings.Contains(LayerName("parent", l, 1), "deadbeef") {
		t.Error("the secret leaked into the layer name")
	}
}
