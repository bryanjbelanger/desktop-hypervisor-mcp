package provider

import (
	"slices"
	"testing"
)

func TestNormalizeEnvConsistent(t *testing.T) {
	env := []string{"PATH=/usr/bin", "USER=alice", "LOGNAME=alice", "HOME=/home/alice"}
	if got := normalizeEnv(env, "alice", "/home/alice"); got != nil {
		t.Fatalf("consistent env should return nil (inherit), got %v", got)
	}
}

func TestNormalizeEnvWrongUser(t *testing.T) {
	// The in-guest failure mode: vmrun guest exec left root's USER/LOGNAME
	// while the effective user is vagrant.
	env := []string{"PATH=/usr/bin", "USER=root", "LOGNAME=root", "HOME=/home/vagrant"}
	got := normalizeEnv(env, "vagrant", "/home/vagrant")
	if got == nil {
		t.Fatal("inconsistent USER/LOGNAME should produce a normalized env")
	}
	for _, want := range []string{"USER=vagrant", "LOGNAME=vagrant", "HOME=/home/vagrant", "PATH=/usr/bin"} {
		if !slices.Contains(got, want) {
			t.Errorf("normalized env missing %q: %v", want, got)
		}
	}
	for _, stale := range []string{"USER=root", "LOGNAME=root"} {
		if slices.Contains(got, stale) {
			t.Errorf("normalized env still carries %q: %v", stale, got)
		}
	}
}

func TestNormalizeEnvMissingAll(t *testing.T) {
	got := normalizeEnv([]string{"PATH=/usr/bin"}, "svc", "/var/lib/svc")
	for _, want := range []string{"USER=svc", "LOGNAME=svc", "HOME=/var/lib/svc"} {
		if !slices.Contains(got, want) {
			t.Errorf("normalized env missing %q: %v", want, got)
		}
	}
}

func TestNormalizeEnvRespectsExplicitHome(t *testing.T) {
	// An explicitly set HOME relocates VirtualBox's per-user config on
	// purpose; only USER/LOGNAME are corrected.
	env := []string{"USER=root", "LOGNAME=root", "HOME=/srv/vbox-home"}
	got := normalizeEnv(env, "vagrant", "/home/vagrant")
	if !slices.Contains(got, "HOME=/srv/vbox-home") {
		t.Errorf("explicit HOME was not preserved: %v", got)
	}
	if slices.Contains(got, "HOME=/home/vagrant") {
		t.Errorf("HOME was overridden: %v", got)
	}
}

func TestNormalizeEnvEmptyHome(t *testing.T) {
	// HOME present but empty counts as missing.
	env := []string{"USER=alice", "LOGNAME=alice", "HOME="}
	got := normalizeEnv(env, "alice", "/home/alice")
	if got == nil {
		t.Fatal("empty HOME should produce a normalized env")
	}
	if !slices.Contains(got, "HOME=/home/alice") {
		t.Errorf("HOME not filled in: %v", got)
	}
	if slices.Contains(got, "HOME=") {
		t.Errorf("empty HOME entry not removed: %v", got)
	}
}
