package project

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func checkout(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const dxForgefile = "# dx\n" +
	"MEMORY 3GiB\n" +
	"CPUS 4\n" +
	"\n" +
	"RUN sudo apt-get install -y default-jdk\n" +
	"\n" +
	"COPY .ruby-version .\n" +
	"RUN <<SH\n" +
	"ruby-build \"$(cat .ruby-version)\" /opt/ci/ruby\n" +
	"echo done\n" +
	"SH\n" +
	"\n" +
	"ENV BUNDLE_JOBS=4\n" +
	"COPY Gemfile Gemfile.lock .\n" +
	"RUN bundle install\n"

func dxCheckout(t *testing.T) string {
	return checkout(t, map[string]string{
		DefaultFile:     dxForgefile,
		".ruby-version": "3.4.1\n",
		"Gemfile":       "source 'https://rubygems.org'\n",
		"Gemfile.lock":  "GEM\n",
	})
}

func TestRunIsTheLayerBoundary(t *testing.T) {
	p, err := Load(dxCheckout(t), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if p.Build.Memory != "3GiB" || p.Build.CPUs != 4 {
		t.Errorf("memory=%q cpus=%d", p.Build.Memory, p.Build.CPUs)
	}
	if len(p.Layers) != 3 {
		t.Fatalf("read %d layers, want one per RUN: %+v", len(p.Layers), p.Layers)
	}
	if len(p.Layers[0].Files) != 0 {
		t.Errorf("the first RUN copies nothing, got %+v", p.Layers[0].Files)
	}
	if len(p.Layers[1].Files) != 1 || p.Layers[1].Files[0].Dest != "/forge/build/.ruby-version" {
		t.Errorf("COPY did not fold into the RUN after it: %+v", p.Layers[1].Files)
	}
	if len(p.Layers[2].Files) != 2 {
		t.Errorf("layer 3 copied %d files, want Gemfile and Gemfile.lock", len(p.Layers[2].Files))
	}
}

func TestAHeredocRunKeepsItsLines(t *testing.T) {
	p, err := Load(dxCheckout(t), "")
	if err != nil {
		t.Fatal(err)
	}
	got := p.Layers[1].Run
	if !strings.Contains(got, "ruby-build") || !strings.Contains(got, "\necho done") {
		t.Errorf("heredoc body = %q, want both lines verbatim", got)
	}
	if strings.Contains(got, "SH") {
		t.Errorf("the terminator leaked into the script: %q", got)
	}
}

func TestEnvCarriesForwardAndKeepsItsOrder(t *testing.T) {
	dir := checkout(t, map[string]string{DefaultFile: "ENV A=1\n" +
		"ENV PATH=/opt/ci/bin:$PATH B=\"two words\"\n" +
		"RUN first\n" +
		"RUN second\n"})
	p, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for i, l := range p.Layers {
		if len(l.Env) != 3 {
			t.Fatalf("layer %d has %d env vars, want all three in scope: %+v", i+1, len(l.Env), l.Env)
		}
	}
	want := []EnvVar{{"A", "1"}, {"PATH", "/opt/ci/bin:$PATH"}, {"B", "two words"}}
	for i, e := range p.Layers[0].Env {
		if e != want[i] {
			t.Errorf("env[%d] = %+v, want %+v", i, e, want[i])
		}
	}
}

func TestWorkdirAppliesToWhatFollowsIt(t *testing.T) {
	dir := checkout(t, map[string]string{
		DefaultFile: "COPY a .\nWORKDIR /app\nCOPY b .\nRUN true\n",
		"a":         "a\n",
		"b":         "b\n",
	})
	p, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	dests := map[string]bool{}
	for _, f := range p.Layers[0].Files {
		dests[f.Dest] = true
	}
	if !dests["/forge/build/a"] || !dests["/app/b"] {
		t.Errorf("dests = %v, want a under the default workdir and b under /app", dests)
	}
	if p.Layers[0].Dir() != "/app" {
		t.Errorf("the RUN happens in %q, want the workdir in effect when it was reached", p.Layers[0].Dir())
	}
}

func TestCopyDestinationsFollowDockerRules(t *testing.T) {
	dir := checkout(t, map[string]string{
		DefaultFile: "COPY db/seed.sql /tmp/seed.sql\n" +
			"COPY Gemfile Gemfile.lock /opt/app/\n" +
			"COPY config /opt/config\n" +
			"RUN true\n",
		"db/seed.sql":         "sql\n",
		"Gemfile":             "gems\n",
		"Gemfile.lock":        "lock\n",
		"config/database.yml": "db\n",
		"config/sub/x.yml":    "x\n",
	})
	p, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range p.Layers[0].Files {
		got = append(got, f.Dest)
	}
	want := []string{
		"/opt/app/Gemfile", "/opt/app/Gemfile.lock",
		"/opt/config/database.yml", "/opt/config/sub/x.yml",
		"/tmp/seed.sql",
	}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("resolved\n  %v\nwant\n  %v", got, want)
	}
}

// Anything staged after the last RUN is still in the image, so it needs a layer
// of its own rather than being silently dropped.
func TestATrailingCopyStillBecomesALayer(t *testing.T) {
	dir := checkout(t, map[string]string{
		DefaultFile: "RUN true\nCOPY late.txt /opt/late.txt\n",
		"late.txt":  "late\n",
	})
	p, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Layers) != 2 {
		t.Fatalf("read %d layers, want the RUN and the trailing COPY", len(p.Layers))
	}
	if p.Layers[1].Run != "" || len(p.Layers[1].Files) != 1 {
		t.Errorf("the trailing layer is wrong: %+v", p.Layers[1])
	}
}

func TestACustomForgefileIsRead(t *testing.T) {
	dir := checkout(t, map[string]string{"Forgefile.staging": "RUN true\n"})
	p, err := Load(dir, "Forgefile.staging")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(p.Layers) != 1 {
		t.Errorf("read %d layers from the named file, want 1", len(p.Layers))
	}
}

func TestCopyCarriesContentNotJustAName(t *testing.T) {
	p, err := Load(dxCheckout(t), "")
	if err != nil {
		t.Fatal(err)
	}
	f := p.Layers[1].Files[0]
	if f.Digest == "" {
		t.Fatal("no digest; a layer that cannot be keyed on content cannot be shared")
	}
	body, err := os.ReadFile(f.Source)
	if err != nil || string(body) != "3.4.1\n" {
		t.Errorf("source %q does not point at the file: %q %v", f.Source, body, err)
	}
}

func TestGlobsResolve(t *testing.T) {
	dir := checkout(t, map[string]string{
		DefaultFile:     "COPY *.gemspec .\nRUN true\n",
		"forge.gemspec": "spec\n",
		"other.gemspec": "other\n",
	})
	p, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Layers[0].Files) != 2 {
		t.Errorf("glob matched %d files, want 2", len(p.Layers[0].Files))
	}
}

// Two clones of one commit have different packfiles, so a .git in a layer would
// give every machine a different image for identical source.
func TestWalkingADirectorySkipsGit(t *testing.T) {
	dir := checkout(t, map[string]string{
		DefaultFile:          "COPY app /opt/app\nRUN true\n",
		"app/main.rb":        "puts 1\n",
		"app/.git/objects/x": "packfile\n",
	})
	p, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range p.Layers[0].Files {
		if strings.Contains(f.Dest, ".git") {
			t.Errorf("copied %s", f.Dest)
		}
	}
}

func TestOnlyTheExecutableBitSurvives(t *testing.T) {
	dir := checkout(t, map[string]string{
		DefaultFile: "COPY bin/setup README .\nRUN true\n",
		"bin/setup": "#!/bin/sh\n",
		"README":    "hi\n",
	})
	if err := os.Chmod(filepath.Join(dir, "bin/setup"), 0o741); err != nil {
		t.Fatal(err)
	}
	p, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	modes := map[string]string{}
	for _, f := range p.Layers[0].Files {
		modes[f.Dest] = f.Mode.String()
	}
	if modes["/forge/build/setup"] != "-rwxr-xr-x" {
		t.Errorf("bin/setup mode = %s, want it normalised to 0755", modes["/forge/build/setup"])
	}
	if modes["/forge/build/README"] != "-rw-r--r--" {
		t.Errorf("README mode = %s, want it normalised to 0644", modes["/forge/build/README"])
	}
}

// A symlink could point out of the checkout, and following one would put a file
// in the layer that is not in the repository.
func TestASymlinkIsRefusedRatherThanFollowed(t *testing.T) {
	dir := checkout(t, map[string]string{
		DefaultFile:    "COPY config /opt/config\nRUN true\n",
		"config/a.yml": "a\n",
		"secret":       "s3cret\n",
	})
	if err := os.Symlink(filepath.Join(dir, "secret"), filepath.Join(dir, "config", "link.yml")); err != nil {
		t.Fatal(err)
	}
	_, err := Load(dir, "")
	if err == nil || !strings.Contains(err.Error(), "link.yml") {
		t.Errorf("error = %v, want it to name the symlink it refused", err)
	}
}

// Someone will paste a Dockerfile in. The error should say what forge does
// instead, not "unknown instruction".
func TestDockerfileOnlyInstructionsAreRefusedByName(t *testing.T) {
	for verb, want := range map[string]string{
		"FROM debian:13":       "base image",
		"CMD bash":             "not a service",
		"ENTRYPOINT /bin/sh":   "not a service",
		"ADD x.tar.gz /opt":    "use COPY",
		"EXPOSE 3000":          "no ports",
		"USER root":            "sudo",
		"ARG VERSION=1":        "identity",
		"VOLUME /data":         "whole disk",
		"HEALTHCHECK CMD true": "health check",
	} {
		_, err := Load(checkout(t, map[string]string{DefaultFile: verb + "\nRUN true\n"}), "")
		if err == nil {
			t.Errorf("%s: expected an error", verb)
			continue
		}
		if !strings.Contains(err.Error(), want) {
			t.Errorf("%s: error = %v, want it to explain (%q)", verb, err, want)
		}
	}
}

func TestLoadRejectsABadForgefile(t *testing.T) {
	cases := map[string]string{
		"empty":               "\n# only a comment\n",
		"unknown instruction": "INSTALL jq\n",
		"relative workdir":    "WORKDIR build\nRUN true\n",
		"bad memory":          "MEMORY plenty\nRUN true\n",
		"bad cpus":            "CPUS many\nRUN true\n",
		"run with no command": "RUN\n",
		"copy with one arg":   "COPY Gemfile\nRUN true\n",
		"copy misses":         "COPY Gemfile.lock .\nRUN true\n",
		"copy escapes":        "COPY ../../etc/passwd .\nRUN true\n",
		"copy absolute src":   "COPY /etc/passwd .\nRUN true\n",
		"unclosed heredoc":    "RUN <<SH\necho hi\n",
		"env with no value":   "ENV LONELY\nRUN true\n",
	}
	for name, body := range cases {
		if _, err := Load(checkout(t, map[string]string{DefaultFile: body}), ""); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

func TestErrorsNameTheLine(t *testing.T) {
	dir := checkout(t, map[string]string{DefaultFile: "RUN true\n\n# a comment\nEXPOSE 3000\n"})
	_, err := Load(dir, "")
	if err == nil || !strings.Contains(err.Error(), ":4:") {
		t.Errorf("error = %v, want it to point at line 4", err)
	}
}

func TestContinuationsAreJoined(t *testing.T) {
	dir := checkout(t, map[string]string{DefaultFile: "RUN apt-get install -y \\\n    jq \\\n    curl\n"})
	p, err := Load(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if p.Layers[0].Run != "apt-get install -y jq curl" {
		t.Errorf("run = %q", p.Layers[0].Run)
	}
}

func TestMissingForgefileSaysSo(t *testing.T) {
	_, err := Load(t.TempDir(), "")
	if err == nil || !strings.Contains(err.Error(), DefaultFile) {
		t.Errorf("error = %v, want it to name %s", err, DefaultFile)
	}
}

func TestMemoryKeepsItsUnit(t *testing.T) {
	cases := map[string]float64{"3GiB": 3, "4": 4, "3000MiB": 2.9296875}
	for value, want := range cases {
		p, err := Load(checkout(t, map[string]string{
			DefaultFile: "MEMORY " + value + "\nRUN true\n",
		}), "")
		if err != nil {
			t.Fatalf("%s: %v", value, err)
		}
		got, ok := p.MemoryGiB()
		if !ok || got != want {
			t.Errorf("%s -> %v (ok=%v), want %v; the awk parser it replaces stripped the unit", value, got, ok, want)
		}
	}
}

func TestSizeInstructionsNeedAValue(t *testing.T) {
	for _, verb := range []string{"MEMORY", "CPUS", "DISK"} {
		_, err := Load(checkout(t, map[string]string{DefaultFile: verb + "\nRUN true\n"}), "")
		if err == nil || !strings.Contains(err.Error(), verb) {
			t.Errorf("%s alone: error = %v, want it refused rather than silently ignored", verb, err)
		}
	}
}
