package main

import (
	"archive/tar"
	"compress/gzip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFixtureBox builds a minimal .box (gzipped tar with box.ovf + disk).
func writeFixtureBox(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	gz := gzip.NewWriter(f)
	defer gz.Close()
	tw := tar.NewWriter(gz)
	defer tw.Close()
	for name, content := range entries {
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(content)); err != nil {
			t.Fatal(err)
		}
	}
}

func TestExtractBox(t *testing.T) {
	dir := t.TempDir()
	box := filepath.Join(dir, "test.box")
	writeFixtureBox(t, box, map[string]string{
		"box.ovf":       "<Envelope/>",
		"disk001.vmdk":  "fakedisk",
		"metadata.json": `{"provider":"virtualbox"}`,
	})
	ovf, err := extractBox(box, filepath.Join(dir, "out"))
	if err != nil {
		t.Fatalf("extractBox: %v", err)
	}
	if filepath.Base(ovf) != "box.ovf" {
		t.Fatalf("expected box.ovf, got %s", ovf)
	}
	data, err := os.ReadFile(ovf)
	if err != nil || string(data) != "<Envelope/>" {
		t.Fatalf("ovf content wrong: %q err=%v", data, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "out", "disk001.vmdk")); err != nil {
		t.Fatalf("disk not extracted: %v", err)
	}
}

func TestExtractBoxRejectsTraversal(t *testing.T) {
	dir := t.TempDir()
	box := filepath.Join(dir, "evil.box")
	writeFixtureBox(t, box, map[string]string{
		"../escape.ovf": "<Envelope/>",
	})
	if _, err := extractBox(box, filepath.Join(dir, "out")); err == nil || !strings.Contains(err.Error(), "unsafe path") {
		t.Fatalf("expected unsafe-path rejection, got %v", err)
	}
}

func TestExtractBoxNoOVF(t *testing.T) {
	dir := t.TempDir()
	box := filepath.Join(dir, "noovf.box")
	writeFixtureBox(t, box, map[string]string{"metadata.json": "{}"})
	if _, err := extractBox(box, filepath.Join(dir, "out")); err == nil || !strings.Contains(err.Error(), "no .ovf") {
		t.Fatalf("expected no-ovf error, got %v", err)
	}
}

func TestFindSource(t *testing.T) {
	if _, err := findSource("debian"); err != nil {
		t.Fatalf("catalog entry debian: %v", err)
	}
	s, err := findSource("vagrant:bento/ubuntu-22.04")
	if err != nil || s.Kind != kindVagrant || s.Locator != "bento/ubuntu-22.04" {
		t.Fatalf("passthrough failed: %+v err=%v", s, err)
	}
	if _, err := findSource("vagrant:nope"); err == nil {
		t.Fatal("expected error for malformed passthrough")
	}
	if _, err := findSource("plan9"); err == nil {
		t.Fatal("expected error for unknown image")
	}
}
