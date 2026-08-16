// Fetch: download, verify, and unpack the artifact a Resolve selected.
// Unlike resolution, these mechanics are provider-neutral — a checksum, a
// GitHub release, or a tar extraction works the same for every hypervisor.
// The provider choice was already baked into Resolved (which asset, which
// Vagrant provider name); Fetch just executes it.
package artifact

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider"
)

// DefaultDir is the image cache. It is deliberately not a hypervisor's VM
// directory: the same ISO can seed VMs on both families, so the cache is
// shared and provider-neutral.
func DefaultDir() string {
	if d := os.Getenv("HV_IMAGE_DIR"); d != "" {
		return d
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, ".hypervisor-images")
}

// FetchResult is what a completed (or dry-run) fetch reports.
type FetchResult struct {
	Path     string               `json:"path"`              // artifact to import or attach
	Format   provider.ImageFormat `json:"format"`            // what Path actually is
	Version  string               `json:"version,omitempty"` // resolved version/tag
	SHA256   string               `json:"sha256,omitempty"`
	Verified bool                 `json:"verified"` // false = no published checksum
	Summary  string               `json:"summary"`
}

// Fetch downloads the resolved artifact into dir (DefaultDir when empty),
// verifying against the source's published checksum where one exists and
// extracting Vagrant boxes down to their importable machine file. A dry run
// resolves version, URL, size, and checksum without downloading.
func Fetch(ctx context.Context, r *Resolved, version, dir string, dryRun bool) (*FetchResult, error) {
	if dir == "" {
		dir = DefaultDir()
	}
	switch r.Kind {

	case KindGitHubAsset:
		rel, err := githubRelease(ctx, r.Locator, version)
		if err != nil {
			return nil, err
		}
		asset, err := rel.findAsset(r.Asset)
		if err != nil {
			return nil, err
		}
		dest := filepath.Join(dir, fmt.Sprintf("%s-%s-%s", r.Source, rel.TagName, asset.Name))
		res := &FetchResult{Path: dest, Format: r.Format, Version: rel.TagName,
			SHA256: asset.sha256(), Verified: asset.sha256() != ""}
		if dryRun {
			res.Summary = fmt.Sprintf("resolved %s %s from %s\n  url: %s\n  size: %.1f MB\n  sha256: %s\n  would save to: %s",
				r.Source, rel.TagName, r.Locator, asset.BrowserDownloadURL,
				float64(asset.Size)/1024/1024, asset.sha256(), dest)
			return res, nil
		}
		out, err := fetchHTTPS(ctx, asset.BrowserDownloadURL, dest, asset.sha256())
		if err != nil {
			return nil, err
		}
		res.Summary = fmt.Sprintf("%s\nversion: %s", out, rel.TagName)
		return res, nil

	case KindTalosFactory:
		// Image Factory builds an image for every released Talos tag but has
		// no "latest" notion of its own, so version discovery reuses the
		// GitHub release feed (which also excludes prereleases). The factory
		// publishes no checksum, so the download is unverified — unlike the
		// pre-v1.8.0 GitHub assets this replaces.
		tag := version
		if tag == "" || tag == "latest" {
			rel, err := githubRelease(ctx, talosRepo, "")
			if err != nil {
				return nil, err
			}
			tag = rel.TagName
		} else if !strings.HasPrefix(tag, "v") {
			tag = "v" + tag
		}
		dlURL := talosFactoryURL(r.Locator, tag, r.Asset)
		dest := filepath.Join(dir, fmt.Sprintf("%s-%s-%s", r.Source, tag, r.Asset))
		res := &FetchResult{Path: dest, Format: r.Format, Version: tag}
		if dryRun {
			sz := "unknown"
			if size := headSize(ctx, dlURL); size > 0 {
				sz = fmt.Sprintf("%.1f MB", float64(size)/1024/1024)
			}
			res.Summary = fmt.Sprintf("resolved %s %s from Image Factory\n  url: %s\n  size: %s\n  no published checksum — would download UNVERIFIED\n  would save to: %s",
				r.Source, tag, dlURL, sz, dest)
			return res, nil
		}
		out, err := fetchHTTPS(ctx, dlURL, dest, "")
		if err != nil {
			return nil, err
		}
		res.Summary = fmt.Sprintf("%s\nversion: %s\nWARNING: Image Factory publishes no checksum — unverified", out, tag)
		return res, nil

	case KindUbuntuCloud:
		arch := r.Arch
		if arch == "" {
			arch = "amd64"
		}
		dlURL, sum, fname, err := ubuntuCloudResolve(ctx, version, arch)
		if err != nil {
			return nil, err
		}
		dest := filepath.Join(dir, fname)
		res := &FetchResult{Path: dest, Format: r.Format, SHA256: sum, Verified: true}
		if dryRun {
			res.Summary = fmt.Sprintf("resolved %s\n  url: %s\n  sha256: %s\n  would save to: %s",
				r.Source, dlURL, sum, dest)
			return res, nil
		}
		out, err := fetchHTTPS(ctx, dlURL, dest, sum)
		if err != nil {
			return nil, err
		}
		res.Summary = out
		return res, nil

	case KindURL:
		dest := filepath.Join(dir, r.Source+"."+string(r.Format))
		res := &FetchResult{Path: dest, Format: r.Format}
		if dryRun {
			sz := "unknown"
			if size := headSize(ctx, r.Locator); size > 0 {
				sz = fmt.Sprintf("%.1f MB", float64(size)/1024/1024)
			}
			res.Summary = fmt.Sprintf("resolved %s\n  url: %s\n  size: %s\n  no published checksum — would download UNVERIFIED\n  would save to: %s",
				r.Source, r.Locator, sz, dest)
			return res, nil
		}
		out, err := fetchHTTPS(ctx, r.Locator, dest, "")
		if err != nil {
			return nil, err
		}
		res.Summary = out + "\nWARNING: no published checksum — unverified"
		return res, nil

	case KindVagrant:
		if r.VagrantProvider == "" {
			return nil, fmt.Errorf("resolved vagrant artifact carries no provider name")
		}
		arch := r.Arch
		if arch == "" {
			arch = "amd64"
		}
		ver, p, err := vagrantResolve(ctx, r.Locator, version, r.VagrantProvider, arch)
		if err != nil {
			return nil, err
		}
		sum := ""
		if strings.EqualFold(p.ChecksumType, "sha256") && p.Checksum != "" {
			sum = p.Checksum
		}
		// The same box name exists once per (provider, arch), so both are part
		// of the cache key.
		base := fmt.Sprintf("%s-%s-%s-%s", strings.ReplaceAll(r.Locator, "/", "-"), ver, r.VagrantProvider, arch)
		boxPath := filepath.Join(dir, "boxes", base+".box")
		extractDir := filepath.Join(dir, "boxes", base)
		res := &FetchResult{Path: extractDir, Format: r.Format, Version: ver,
			SHA256: sum, Verified: sum != ""}
		if dryRun {
			sumNote := "registry publishes no sha256 for this version — would download UNVERIFIED"
			if sum != "" {
				sumNote = "sha256: " + sum
			}
			res.Summary = fmt.Sprintf("resolved vagrant box %s %s (%s provider, %s)\n  url: %s\n  %s\n  would save to: %s and extract to: %s",
				r.Locator, ver, r.VagrantProvider, arch, p.DownloadURL, sumNote, boxPath, extractDir)
			return res, nil
		}
		out, err := fetchHTTPS(ctx, p.DownloadURL, boxPath, sum)
		if err != nil {
			return nil, err
		}
		machine, err := extractBox(boxPath, extractDir)
		if err != nil {
			return nil, err
		}
		res.Path = machine
		res.Format = formatOf(machine, r.Format)
		if sum == "" {
			out += "\nWARNING: registry publishes no sha256 for this version — unverified"
		}
		res.Summary = fmt.Sprintf("%s\nextracted: %s", out, extractDir)
		return res, nil
	}
	return nil, fmt.Errorf("unhandled artifact kind %s", r.Kind)
}

// talosFactoryURL is the Image Factory download URL for a schematic ID, a
// version tag ("v1.13.8"), and a target asset name ("vmware-amd64.ova").
func talosFactoryURL(schematic, tag, asset string) string {
	return "https://factory.talos.dev/image/" + schematic + "/" + tag + "/" + asset
}

// formatOf maps a machine file's extension to its format, falling back to the
// resolved format when the extension is unrecognized.
func formatOf(path string, fallback provider.ImageFormat) provider.ImageFormat {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".ovf":
		return provider.FormatOVF
	case ".vmx":
		return provider.FormatVMX
	case ".ova":
		return provider.FormatOVA
	}
	return fallback
}

// ------------------------------------------------------------ verified download

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// fetchHTTPS streams an https URL to destPath (creating parent dirs), verifying
// the sha256 when provided. An existing file with a matching checksum is a no-op.
func fetchHTTPS(ctx context.Context, rawURL, destPath, wantSHA string) (string, error) {
	u, err := url.Parse(rawURL)
	if err != nil || u.Scheme != "https" {
		return "", fmt.Errorf("only https URLs are allowed")
	}
	want := strings.ToLower(strings.TrimSpace(wantSHA))
	if want != "" {
		if sum, err := fileSHA256(destPath); err == nil && sum == want {
			return fmt.Sprintf("already present with matching sha256: %s", destPath), nil
		}
	}
	if err := os.MkdirAll(filepath.Dir(destPath), 0o755); err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", err
	}
	client := httpClient(30 * time.Minute)
	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("download failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("download failed: HTTP %s", resp.Status)
	}

	tmp := destPath + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return "", err
	}
	h := sha256.New()
	start := time.Now()
	n, err := io.Copy(io.MultiWriter(f, h), resp.Body)
	closeErr := f.Close()
	if err != nil || closeErr != nil {
		os.Remove(tmp)
		if err == nil {
			err = closeErr
		}
		return "", fmt.Errorf("download failed after %d bytes: %w", n, err)
	}
	got := hex.EncodeToString(h.Sum(nil))
	if want != "" && got != want {
		os.Remove(tmp)
		return "", fmt.Errorf("sha256 mismatch: expected %s, got %s (file discarded)", want, got)
	}
	if err := os.Rename(tmp, destPath); err != nil {
		os.Remove(tmp)
		return "", err
	}
	return fmt.Sprintf("downloaded %s → %s (%.1f MB in %s, sha256 %s)",
		rawURL, destPath, float64(n)/1024/1024, time.Since(start).Round(time.Second), got), nil
}

// headSize reports Content-Length after redirects (0 if unknown).
func headSize(ctx context.Context, rawURL string) int64 {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, rawURL, nil)
	if err != nil {
		return 0
	}
	client := httpClient(30 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return 0
	}
	resp.Body.Close()
	return resp.ContentLength
}

// ------------------------------------------------------- GitHub release lookup

type ghAsset struct {
	Name               string `json:"name"`
	Size               int64  `json:"size"`
	Digest             string `json:"digest"` // "sha256:<hex>", set by GitHub
	BrowserDownloadURL string `json:"browser_download_url"`
}

type ghRelease struct {
	TagName string    `json:"tag_name"`
	Assets  []ghAsset `json:"assets"`
}

// githubRelease fetches release metadata for repo ("owner/name"); version is a
// tag like "v1.13.7" ("" or "latest" resolves the latest release).
func githubRelease(ctx context.Context, repo, version string) (*ghRelease, error) {
	api := "https://api.github.com/repos/" + repo + "/releases/latest"
	if version != "" && version != "latest" {
		if !strings.HasPrefix(version, "v") {
			version = "v" + version
		}
		api = "https://api.github.com/repos/" + repo + "/releases/tags/" + version
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	client := httpClient(30 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("GitHub API request failed: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("GitHub API: HTTP %s for %s", resp.Status, api)
	}
	var rel ghRelease
	if err := json.NewDecoder(resp.Body).Decode(&rel); err != nil {
		return nil, err
	}
	return &rel, nil
}

func (r *ghRelease) findAsset(name string) (*ghAsset, error) {
	for i := range r.Assets {
		if r.Assets[i].Name == name {
			return &r.Assets[i], nil
		}
	}
	names := make([]string, len(r.Assets))
	for i, a := range r.Assets {
		names[i] = a.Name
	}
	return nil, fmt.Errorf("release %s has no asset %q (assets: %v)", r.TagName, name, names)
}

func (a *ghAsset) sha256() string {
	return strings.TrimPrefix(a.Digest, "sha256:")
}

// ------------------------------------------------------------- vagrant registry

type vagrantProviderEntry struct {
	Name         string `json:"name"`
	Architecture string `json:"architecture"`
	DownloadURL  string `json:"download_url"`
	Checksum     string `json:"checksum"`
	ChecksumType string `json:"checksum_type"`
}

type vagrantVersion struct {
	Version   string                 `json:"version"`
	Providers []vagrantProviderEntry `json:"providers"`
}

type vagrantBox struct {
	CurrentVersion vagrantVersion   `json:"current_version"`
	Versions       []vagrantVersion `json:"versions"`
}

// provider selects the entry for a provider name and host architecture.
// Vagrant Cloud publishes one entry per (provider, architecture) pair and
// marks a default — often arm64 — so taking the first name match would hand
// an Intel host an ARM disk image. Boxes from before multi-arch report
// "unknown" (or nothing) and are accepted as the fallback.
func (v vagrantVersion) provider(name, arch string) *vagrantProviderEntry {
	var fallback *vagrantProviderEntry
	for i := range v.Providers {
		p := &v.Providers[i]
		if p.Name != name {
			continue
		}
		if p.Architecture == arch {
			return p
		}
		if fallback == nil && (p.Architecture == "" || p.Architecture == "unknown") {
			fallback = p
		}
	}
	return fallback
}

// vagrantResolve finds the artifact for a box version ("" = current) matching
// both the provider name (from Kind.VagrantProvider — boxes are published once
// per provider) and the host architecture.
func vagrantResolve(ctx context.Context, box, version, providerName, arch string) (string, *vagrantProviderEntry, error) {
	api := "https://app.vagrantup.com/api/v2/box/" + box
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, api, nil)
	if err != nil {
		return "", nil, err
	}
	client := httpClient(30 * time.Second)
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
	if version == "" || version == "latest" {
		if p := b.CurrentVersion.provider(providerName, arch); p != nil {
			return b.CurrentVersion.Version, p, nil
		}
		return "", nil, fmt.Errorf("box %s has no %s/%s artifact in current version", box, providerName, arch)
	}
	for _, v := range b.Versions {
		if v.Version == version {
			if p := v.provider(providerName, arch); p != nil {
				return v.Version, p, nil
			}
			return "", nil, fmt.Errorf("box %s %s has no %s/%s artifact", box, version, providerName, arch)
		}
	}
	return "", nil, fmt.Errorf("box %s has no version %s", box, version)
}

// extractBox unpacks a .box (gzipped or plain tar) into destDir and returns
// the contained machine file: box.ovf for virtualbox boxes, the .vmx for
// vmware_desktop boxes.
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
	machine := ""
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
			switch {
			case strings.HasSuffix(name, ".ovf"):
				machine = dest
			case strings.HasSuffix(name, ".vmx") && machine == "":
				machine = dest
			}
		}
	}
	if machine == "" {
		return "", fmt.Errorf("no .ovf or .vmx found in %s after extraction", boxPath)
	}
	return machine, nil
}

// ---------------------------------------------------------- ubuntu cloud images

// ubuntuCloudResolve returns the OVA URL and its sha256 from Canonical's
// published SHA256SUMS for a release.
func ubuntuCloudResolve(ctx context.Context, version, arch string) (dlURL, sum, fname string, err error) {
	if version == "" || version == "latest" {
		version = "24.04"
	}
	base := "https://cloud-images.ubuntu.com/releases/" + version + "/release/"
	fname = fmt.Sprintf("ubuntu-%s-server-cloudimg-%s.ova", version, arch)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"SHA256SUMS", nil)
	if err != nil {
		return "", "", "", err
	}
	client := httpClient(30 * time.Second)
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
	sum = findSHA256Sum(string(body), fname)
	if sum == "" {
		return "", "", "", fmt.Errorf("%s not listed in SHA256SUMS for %s", fname, version)
	}
	return base + fname, sum, fname, nil
}

// findSHA256Sum scans coreutils-style checksum lines ("<hex>  <name>") for a
// filename, tolerating the binary-mode "*" prefix.
func findSHA256Sum(sums, fname string) string {
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == fname {
			return fields[0]
		}
	}
	return ""
}
