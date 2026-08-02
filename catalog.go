// Image catalog: official, maintained VM images resolvable by short name,
// with per-source verification. Direct OVA publishers are used where they
// exist; the Vagrant box registry fills the gaps (a VirtualBox .box is a
// gzipped tar containing box.ovf + disks — importable after extraction).
package main

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type sourceKind string

const (
	kindGitHubOVA   sourceKind = "github-ova"   // .ova asset on a GitHub release
	kindGitHubISO   sourceKind = "github-iso"   // named .iso asset on a GitHub release
	kindUbuntuCloud sourceKind = "ubuntu-cloud" // cloud-images.ubuntu.com + SHA256SUMS
	kindURL         sourceKind = "url"          // fixed URL, no version resolution
	kindVagrant     sourceKind = "vagrant"      // Vagrant registry org/box, virtualbox provider
)

type imageSource struct {
	Name           string
	Kind           sourceKind
	Locator        string // github: owner/repo; vagrant: org/box; url: the URL; ubuntu-cloud: unused
	Asset          string // github-iso: exact asset name
	DefaultVersion string
	Desc           string
	Notes          string
}

func imageCatalog() []imageSource {
	return []imageSource{
		{Name: "talos", Kind: kindGitHubOVA, Locator: talosOVARepoName(),
			Desc: "Talos Linux VirtualBox appliance (Kubernetes node OS)"},
		{Name: "talos-iso", Kind: kindGitHubISO, Locator: talosRepo, Asset: "metal-amd64.iso",
			Desc: "Talos Linux official metal ISO (Sidero Labs)"},
		{Name: "ubuntu-cloud", Kind: kindUbuntuCloud, DefaultVersion: "24.04",
			Desc: "Ubuntu Server cloud image OVA (Canonical)",
			Notes: "cloud image: no default login — attach a NoCloud cloud-init seed ISO, or use the 'ubuntu' Vagrant entry instead"},
		{Name: "turnkey-core", Kind: kindURL,
			Locator: "https://mirror.turnkeylinux.org/turnkeylinux/images/ova/turnkey-core-18.1-bookworm-amd64.ova",
			Desc:    "TurnKey Linux Core appliance OVA", Notes: "fixed version pin; no automated checksum"},
		{Name: "windows-dev", Kind: kindURL, Locator: "https://aka.ms/windev_VM_virtualbox",
			Desc:  "Microsoft Windows development environment OVA",
			Notes: "~20GB, evaluation-licensed, Microsoft publishes no checksum — downloaded unverified"},
		// Vagrant registry (virtualbox provider, registry-published sha256 enforced).
		{Name: "ubuntu", Kind: kindVagrant, Locator: "bento/ubuntu-24.04", Desc: "Ubuntu 24.04 LTS (bento)"},
		{Name: "debian", Kind: kindVagrant, Locator: "bento/debian-12", Desc: "Debian 12 (bento)"},
		{Name: "fedora", Kind: kindVagrant, Locator: "bento/fedora-40", Desc: "Fedora 40 (bento)"},
		{Name: "rocky", Kind: kindVagrant, Locator: "bento/rockylinux-10", Desc: "Rocky Linux 10 (bento)",
			Notes: "CentOS Linux successor, bug-for-bug RHEL 10; rocky-9 for the RHEL 9 era"},
		{Name: "rocky-9", Kind: kindVagrant, Locator: "bento/rockylinux-9", Desc: "Rocky Linux 9 (bento)"},
		{Name: "alma", Kind: kindVagrant, Locator: "bento/almalinux-10", Desc: "AlmaLinux 10 (bento)",
			Notes: "CentOS Linux successor, ABI-compatible with RHEL 10; alma-9 for the RHEL 9 era"},
		{Name: "alma-9", Kind: kindVagrant, Locator: "bento/almalinux-9", Desc: "AlmaLinux 9 (bento)"},
		{Name: "centos-stream", Kind: kindVagrant, Locator: "bento/centos-stream-10", Desc: "CentOS Stream 10 (bento)",
			Notes: "rolling preview upstream of RHEL 10; classic CentOS Linux is EOL — see rocky/alma"},
		{Name: "centos-stream-9", Kind: kindVagrant, Locator: "bento/centos-stream-9", Desc: "CentOS Stream 9 (bento)"},
		{Name: "opensuse", Kind: kindVagrant, Locator: "bento/opensuse-leap-15.6", Desc: "openSUSE Leap 15.6 (bento)"},
		{Name: "freebsd", Kind: kindVagrant, Locator: "bento/freebsd-14", Desc: "FreeBSD 14 (bento)"},
		{Name: "kali", Kind: kindVagrant, Locator: "kalilinux/rolling", Desc: "Kali Linux rolling (official)"},
	}
}

func findSource(name string) (*imageSource, error) {
	// "vagrant:org/box" passthrough reaches the whole registry.
	if strings.HasPrefix(name, "vagrant:") {
		loc := strings.TrimPrefix(name, "vagrant:")
		if !strings.Contains(loc, "/") {
			return nil, fmt.Errorf("vagrant passthrough must be 'vagrant:org/box'")
		}
		return &imageSource{Name: name, Kind: kindVagrant, Locator: loc,
			Desc: "Vagrant registry box (passthrough)"}, nil
	}
	for _, s := range imageCatalog() {
		if s.Name == name {
			return &s, nil
		}
	}
	names := make([]string, 0)
	for _, s := range imageCatalog() {
		names = append(names, s.Name)
	}
	return nil, fmt.Errorf("unknown image %q — catalog: %s (or vagrant:org/box)", name, strings.Join(names, ", "))
}

// -------------------------------------------------------------- vagrant boxes

type vagrantProvider struct {
	Name         string `json:"name"`
	DownloadURL  string `json:"download_url"`
	Checksum     string `json:"checksum"`
	ChecksumType string `json:"checksum_type"`
}

type vagrantVersion struct {
	Version   string            `json:"version"`
	Providers []vagrantProvider `json:"providers"`
}

type vagrantBox struct {
	CurrentVersion vagrantVersion   `json:"current_version"`
	Versions       []vagrantVersion `json:"versions"`
}

// vagrantResolve finds the virtualbox provider for a box version ("" = current).
func vagrantResolve(ctx context.Context, box, version string) (ver string, p *vagrantProvider, err error) {
	api := "https://app.vagrantup.com/api/v2/box/" + box
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return "", nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", nil, fmt.Errorf("vagrant registry request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", nil, fmt.Errorf("vagrant registry: HTTP %s for %s", resp.Status, box)
	}
	var b vagrantBox
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		return "", nil, err
	}
	pick := func(v vagrantVersion) *vagrantProvider {
		for i := range v.Providers {
			if v.Providers[i].Name == "virtualbox" {
				return &v.Providers[i]
			}
		}
		return nil
	}
	if version == "" || version == "latest" {
		if p := pick(b.CurrentVersion); p != nil {
			return b.CurrentVersion.Version, p, nil
		}
		return "", nil, fmt.Errorf("box %s has no virtualbox provider in current version", box)
	}
	for _, v := range b.Versions {
		if v.Version == version {
			if p := pick(v); p != nil {
				return v.Version, p, nil
			}
			return "", nil, fmt.Errorf("box %s %s has no virtualbox provider", box, version)
		}
	}
	return "", nil, fmt.Errorf("box %s has no version %s", box, version)
}

// extractBox unpacks a .box (gzipped or plain tar) into destDir and returns
// the path of the contained .ovf.
func extractBox(boxPath, destDir string) (string, error) {
	f, err := os.Open(boxPath)
	if err != nil {
		return "", err
	}
	defer f.Close()
	var r io.Reader = f
	if gz, err := gzip.NewReader(f); err == nil {
		r = gz
		defer gz.Close()
	} else {
		if _, err := f.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
	}
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return "", err
	}
	ovf := ""
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", fmt.Errorf("extracting %s: %w", boxPath, err)
		}
		name := filepath.Clean(hdr.Name)
		if strings.HasPrefix(name, "..") || filepath.IsAbs(name) {
			return "", fmt.Errorf("unsafe path in box archive: %s", hdr.Name)
		}
		dest := filepath.Join(destDir, name)
		switch hdr.Typeflag {
		case tar.TypeDir:
			if err := os.MkdirAll(dest, 0o755); err != nil {
				return "", err
			}
		case tar.TypeReg:
			if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
				return "", err
			}
			out, err := os.Create(dest)
			if err != nil {
				return "", err
			}
			if _, err := io.Copy(out, tr); err != nil {
				out.Close()
				return "", err
			}
			out.Close()
			if strings.HasSuffix(name, ".ovf") {
				ovf = dest
			}
		}
	}
	if ovf == "" {
		return "", fmt.Errorf("no .ovf found in %s after extraction", boxPath)
	}
	return ovf, nil
}

// ------------------------------------------------------------- other resolvers

// ubuntuCloudResolve returns the OVA URL and its sha256 from Canonical's SHA256SUMS.
func ubuntuCloudResolve(ctx context.Context, version string) (dlURL, sum, fname string, err error) {
	if version == "" || version == "latest" {
		version = "24.04"
	}
	base := "https://cloud-images.ubuntu.com/releases/" + version + "/release/"
	fname = "ubuntu-" + version + "-server-cloudimg-amd64.ova"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"SHA256SUMS", nil)
	if err != nil {
		return "", "", "", err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return "", "", "", fmt.Errorf("fetching SHA256SUMS: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", "", fmt.Errorf("SHA256SUMS: HTTP %s (does release %s exist?)", resp.Status, version)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", "", "", err
	}
	for _, line := range strings.Split(string(body), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == fname {
			return base + fname, fields[0], fname, nil
		}
	}
	return "", "", "", fmt.Errorf("%s not listed in SHA256SUMS for %s", fname, version)
}

// headSize reports Content-Length after redirects (0 if unknown).
func headSize(ctx context.Context, rawURL string) int64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return 0
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.ContentLength
}

// ----------------------------------------------------------------- image tool

type imageIn struct {
	Action  string `json:"action" jsonschema:"catalog (list available images) or fetch (resolve/download one)"`
	Name    string `json:"name,omitempty" jsonschema:"catalog name (e.g. talos, ubuntu, debian, kali) or vagrant:org/box passthrough"`
	Version string `json:"version,omitempty" jsonschema:"version/tag where the source supports it (default: latest)"`
	DryRun  bool   `json:"dry_run,omitempty" jsonschema:"resolve version/URL/checksum without downloading"`
}

func imageTool(ctx context.Context, _ *mcp.CallToolRequest, in imageIn) (*mcp.CallToolResult, any, error) {
	switch in.Action {
	case "catalog":
		var b strings.Builder
		b.WriteString("name | kind | source | description\n")
		for _, s := range imageCatalog() {
			note := ""
			if s.Notes != "" {
				note = " — " + s.Notes
			}
			loc := s.Locator
			if s.Kind == kindUbuntuCloud {
				loc = "cloud-images.ubuntu.com"
			}
			fmt.Fprintf(&b, "%s | %s | %s | %s%s\n", s.Name, s.Kind, loc, s.Desc, note)
		}
		b.WriteString("vagrant:org/box | vagrant | app.vagrantup.com | any registry box with a virtualbox provider\n")
		b.WriteString("\nfetch returns a path: .ova → appliance import; .ovf (vagrant) → appliance import; .iso → storage attach as dvddrive")
		return text(b.String()), nil, nil

	case "fetch":
		if in.Name == "" {
			return nil, nil, fmt.Errorf("'name' is required for fetch (see action=catalog)")
		}
		src, err := findSource(in.Name)
		if err != nil {
			return nil, nil, err
		}
		switch src.Kind {

		case kindGitHubOVA, kindGitHubISO:
			rel, err := githubRelease(ctx, src.Locator, in.Version)
			if err != nil {
				return nil, nil, err
			}
			match := func(n string) bool { return strings.HasSuffix(n, ".ova") }
			if src.Kind == kindGitHubISO {
				match = func(n string) bool { return n == src.Asset }
			}
			asset, err := rel.findAsset(match)
			if err != nil {
				return nil, nil, err
			}
			destName := asset.Name
			if src.Kind == kindGitHubISO {
				destName = fmt.Sprintf("%s-%s-%s", strings.ReplaceAll(src.Name, "/", "-"), rel.TagName, asset.Name)
			}
			dest := filepath.Join(imageDir(), destName)
			if in.DryRun {
				return text(fmt.Sprintf("resolved %s %s from %s\n  url: %s\n  size: %.1f MB\n  sha256: %s\n  would save to: %s",
					src.Name, rel.TagName, src.Locator, asset.BrowserDownloadURL,
					float64(asset.Size)/1024/1024, asset.sha256(), dest)), nil, nil
			}
			out, err := fetchHTTPS(ctx, asset.BrowserDownloadURL, dest, asset.sha256())
			if err != nil {
				return nil, nil, err
			}
			return text(fmt.Sprintf("%s\nversion: %s\npath: %s", out, rel.TagName, dest)), nil, nil

		case kindUbuntuCloud:
			dlURL, sum, fname, err := ubuntuCloudResolve(ctx, in.Version)
			if err != nil {
				return nil, nil, err
			}
			dest := filepath.Join(imageDir(), fname)
			if in.DryRun {
				return text(fmt.Sprintf("resolved %s\n  url: %s\n  sha256: %s\n  would save to: %s\n  note: %s",
					src.Name, dlURL, sum, dest, src.Notes)), nil, nil
			}
			out, err := fetchHTTPS(ctx, dlURL, dest, sum)
			if err != nil {
				return nil, nil, err
			}
			return text(fmt.Sprintf("%s\npath: %s\nnote: %s", out, dest, src.Notes)), nil, nil

		case kindURL:
			dest := filepath.Join(imageDir(), src.Name+".ova")
			if in.DryRun {
				size := headSize(ctx, src.Locator)
				sz := "unknown"
				if size > 0 {
					sz = fmt.Sprintf("%.1f MB", float64(size)/1024/1024)
				}
				return text(fmt.Sprintf("resolved %s\n  url: %s\n  size: %s\n  no published checksum — would download UNVERIFIED\n  would save to: %s\n  note: %s",
					src.Name, src.Locator, sz, dest, src.Notes)), nil, nil
			}
			out, err := fetchHTTPS(ctx, src.Locator, dest, "")
			if err != nil {
				return nil, nil, err
			}
			return text(fmt.Sprintf("%s\npath: %s\nWARNING: no published checksum — unverified\nnote: %s", out, dest, src.Notes)), nil, nil

		case kindVagrant:
			ver, p, err := vagrantResolve(ctx, src.Locator, in.Version)
			if err != nil {
				return nil, nil, err
			}
			sum := ""
			sumNote := "registry publishes no sha256 for this version — would download UNVERIFIED"
			if strings.EqualFold(p.ChecksumType, "sha256") && p.Checksum != "" {
				sum = p.Checksum
				sumNote = "sha256: " + sum
			}
			safe := strings.ReplaceAll(src.Locator, "/", "-")
			boxPath := filepath.Join(imageDir(), "boxes", fmt.Sprintf("%s-%s.box", safe, ver))
			extractDir := filepath.Join(imageDir(), "boxes", fmt.Sprintf("%s-%s", safe, ver))
			if in.DryRun {
				return text(fmt.Sprintf("resolved vagrant box %s %s (virtualbox provider)\n  url: %s\n  %s\n  would save to: %s and extract to: %s",
					src.Locator, ver, p.DownloadURL, sumNote, boxPath, extractDir)), nil, nil
			}
			out, err := fetchHTTPS(ctx, p.DownloadURL, boxPath, sum)
			if err != nil {
				return nil, nil, err
			}
			ovf, err := extractBox(boxPath, extractDir)
			if err != nil {
				return nil, nil, err
			}
			return text(fmt.Sprintf("%s\nextracted: %s\nimport with the appliance tool: file_path=%s", out, extractDir, ovf)), nil, nil
		}
		return nil, nil, fmt.Errorf("unhandled source kind %s", src.Kind)
	}
	return nil, nil, fmt.Errorf("unknown action: %s (catalog|fetch)", in.Action)
}
