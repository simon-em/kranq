package peer

import (
	"errors"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestParseTarget(t *testing.T) {
	cases := []struct {
		in   string
		want Target
	}{
		{"macmini@142.127.69.2:333", Target{User: "macmini", Host: "142.127.69.2", Port: 333}},
		{"macmini@host", Target{User: "macmini", Host: "host"}},
		{"host:22", Target{Host: "host", Port: 22}},
		{"host", Target{Host: "host"}},
		{"  host  ", Target{Host: "host"}},
	}
	for _, c := range cases {
		got, err := ParseTarget(c.in)
		if err != nil {
			t.Fatalf("%q: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%q: got %+v, want %+v", c.in, got, c.want)
		}
	}
}

func TestParseTargetRejectsTypos(t *testing.T) {
	for _, in := range []string{"", "   ", "@host", "user@", "host:", "host:0", "host:70000", "host:ssh"} {
		if _, err := ParseTarget(in); err == nil {
			t.Fatalf("%q was accepted", in)
		}
	}
}

func TestSSHArgsCarryThePortAndFailFast(t *testing.T) {
	target, _ := ParseTarget("macmini@host:333")
	got := target.SSHArgs("forge", "version")
	want := []string{"-o", "BatchMode=yes", "-p", "333", "macmini@host", "forge", "version"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	plain, _ := ParseTarget("host")
	if strings.Join(plain.SSHArgs("x"), " ") != "-o BatchMode=yes host x" {
		t.Fatalf("a portless target got a port: %v", plain.SSHArgs("x"))
	}
}

func TestSCPArgsUseCapitalP(t *testing.T) {
	target, _ := ParseTarget("macmini@host:333")
	got := target.SCPArgs("/tmp/forge", "/tmp/forge.new")
	want := []string{"-o", "BatchMode=yes", "-P", "333", "/tmp/forge", "macmini@host:/tmp/forge.new"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("got %v, want %v", got, want)
	}
}

func TestFirstPeerBecomesTheDefault(t *testing.T) {
	r := &Registry{}
	if err := r.Add(Peer{Name: "mini-1", SSH: "u@h"}); err != nil {
		t.Fatal(err)
	}
	p, err := r.Default()
	if err != nil || p.Name != "mini-1" {
		t.Fatalf("got %+v, %v", p, err)
	}
	if p.Bin != DefaultBin {
		t.Fatalf("no default binary path: %q", p.Bin)
	}
}

func TestOnlyOnePeerIsEverTheDefault(t *testing.T) {
	r := &Registry{}
	r.Add(Peer{Name: "mini-1", SSH: "u@h"})
	r.Add(Peer{Name: "mini-2", SSH: "u@h2", Default: true})
	defaults := 0
	for _, p := range r.Peers {
		if p.Default {
			defaults++
		}
	}
	if defaults != 1 {
		t.Fatalf("%d peers claim to be the default", defaults)
	}
	if p, _ := r.Default(); p.Name != "mini-2" {
		t.Fatalf("the default is %s", p.Name)
	}
}

func TestRemovingTheDefaultPromotesAnother(t *testing.T) {
	r := &Registry{}
	r.Add(Peer{Name: "mini-1", SSH: "u@h"})
	r.Add(Peer{Name: "mini-2", SSH: "u@h2"})
	if err := r.Remove("mini-1"); err != nil {
		t.Fatal(err)
	}
	p, err := r.Default()
	if err != nil {
		t.Fatalf("the list was left with no default: %v", err)
	}
	if p.Name != "mini-2" {
		t.Fatalf("promoted %s", p.Name)
	}
}

func TestRemovingTheLastPeerLeavesNothing(t *testing.T) {
	r := &Registry{}
	r.Add(Peer{Name: "mini-1", SSH: "u@h"})
	r.Remove("mini-1")
	if _, err := r.Default(); !errors.Is(err, ErrNoPeers) {
		t.Fatalf("expected ErrNoPeers, got %v", err)
	}
	if err := r.Remove("mini-1"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestAddingTwiceUpdatesRatherThanDuplicates(t *testing.T) {
	r := &Registry{}
	r.Add(Peer{Name: "mini-1", SSH: "u@h:22"})
	r.Add(Peer{Name: "mini-1", SSH: "u@h:333", Bin: "/opt/forge"})
	if len(r.Peers) != 1 {
		t.Fatalf("%d peers", len(r.Peers))
	}
	p, _ := r.Get("mini-1")
	if p.SSH != "u@h:333" || p.Bin != "/opt/forge" {
		t.Fatalf("not updated: %+v", p)
	}
	if !p.Default {
		t.Fatal("updating the only peer dropped its default")
	}
}

func TestAddRejectsBadNamesAndTargets(t *testing.T) {
	r := &Registry{}
	for _, name := range []string{"", "Mini", "mini_1", "-mini", strings.Repeat("m", 33)} {
		if err := r.Add(Peer{Name: name, SSH: "u@h"}); err == nil {
			t.Fatalf("name %q was accepted", name)
		}
	}
	if err := r.Add(Peer{Name: "mini", SSH: "u@h:not-a-port"}); err == nil {
		t.Fatal("an unparseable target was accepted")
	}
	if len(r.Peers) != 0 {
		t.Fatalf("a rejected peer was stored anyway: %+v", r.Peers)
	}
}

func TestSaveAndLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "peers.json")
	r := &Registry{}
	r.Add(Peer{Name: "mini-1", SSH: "macmini@host:333"})
	if err := r.Save(path); err != nil {
		t.Fatal(err)
	}
	back, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(back.Peers, r.Peers) {
		t.Fatalf("got %+v, want %+v", back.Peers, r.Peers)
	}
}

func TestLoadingNothingIsNotAnError(t *testing.T) {
	r, err := Load(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil {
		t.Fatal(err)
	}
	if len(r.Peers) != 0 {
		t.Fatal("peers appeared from nowhere")
	}
}

func TestGetWithNoNameIsTheDefault(t *testing.T) {
	r := &Registry{}
	r.Add(Peer{Name: "mini-1", SSH: "u@h"})
	r.Add(Peer{Name: "mini-2", SSH: "u@h2", Default: true})
	p, err := r.Get("")
	if err != nil || p.Name != "mini-2" {
		t.Fatalf("got %+v, %v", p, err)
	}
}

func TestValidBin(t *testing.T) {
	for _, good := range []string{"/opt/forge", "~/.local/bin/forge", "/Users/x/.local/bin/forge"} {
		if err := ValidBin(good); err != nil {
			t.Fatalf("%q rejected: %v", good, err)
		}
	}
	// A remote path is interpolated into a double-quoted string so a leading ~
	// still expands, which makes these characters a command-injection risk.
	for _, bad := range []string{"", "forge", "./forge", `~/f"oo`, "~/f$oo", "~/f`oo`", `~/f\oo`, "~/f\noo"} {
		if err := ValidBin(bad); err == nil {
			t.Fatalf("%q was accepted as a remote forge path", bad)
		}
	}
}

func TestAddRejectsAnUnsafeBin(t *testing.T) {
	r := &Registry{}
	if err := r.Add(Peer{Name: "mini", SSH: "u@h", Bin: "~/forge\"; rm -rf /"}); err == nil {
		t.Fatal("a bin path carrying shell syntax was accepted")
	}
}
