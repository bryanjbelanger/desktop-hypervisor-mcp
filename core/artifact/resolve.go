// Package artifact resolves OS images to a concrete, downloadable artifact
// for a specific provider and host architecture.
//
// The original VirtualBox-only catalog assumed one artifact per image. That
// assumption does not survive a second provider: Talos publishes a distinct
// OVA per hypervisor platform, Microsoft publishes a per-hypervisor dev VM,
// and Vagrant boxes are published per provider ("virtualbox" boxes are not
// usable by VMware). Resolution is therefore keyed on
// (image, provider kind, host arch) rather than on image alone.
package artifact

import (
	"fmt"
	"runtime"
	"strings"

	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider"
)

// Kind is how an artifact is located and verified.
type Kind string

const (
	KindGitHubAsset  Kind = "github_asset"  // release asset from a GitHub repo
	KindURL          Kind = "url"           // direct download
	KindVagrant      Kind = "vagrant"       // Vagrant registry box, per provider
	KindUbuntuCloud  Kind = "ubuntu_cloud"  // Canonical cloud image index
	KindTalosFactory Kind = "talos_factory" // Talos Image Factory build (factory.talos.dev)
)

// Variant is one concrete artifact for a given provider family and arch.
// Empty Family or Arch means "any" — the fallback when no exact match exists.
type Variant struct {
	Family  string               // "virtualbox", "vmware", or "" for any
	Arch    string               // GOARCH, or "" for any
	Kind    Kind                 // overrides the Source Kind when set; variants of one image can come from different distribution channels
	Locator string               // repo, URL, box name, or schematic ID depending on Kind
	Asset   string               // release asset name, may contain {arch}
	Format  provider.ImageFormat // what the artifact actually is
}

// Source is a named OS image with one or more provider-specific variants.
type Source struct {
	Name     string
	Kind     Kind
	Desc     string
	Notes    string
	Variants []Variant

	DefaultVersion string
}

// Resolved is a single artifact selected for a specific provider.
type Resolved struct {
	Source  string               `json:"source"`
	Kind    Kind                 `json:"kind"`
	Locator string               `json:"locator"`
	Asset   string               `json:"asset,omitempty"`
	Format  provider.ImageFormat `json:"format"`
	Arch    string               `json:"arch"`
	Notes   string               `json:"notes,omitempty"`

	// VagrantProvider is set for KindVagrant: the provider name to request
	// from the box registry.
	VagrantProvider string `json:"vagrant_provider,omitempty"`
}

const talosRepo = "siderolabs/talos"

// talosVanillaSchematic is Image Factory's well-known ID for the empty
// schematic (no extensions, no extra kernel args) — the equivalent of the
// images Talos attached to GitHub releases before v1.8.0.
const talosVanillaSchematic = "376567988ad370138ad8b2698212367b8edcb69b5fd68c80be1f2ec7d603b4ba"

// Catalog is the maintained image list. Variants carry the provider coupling
// that used to be baked into a single VirtualBox-shaped entry.
func Catalog() []Source {
	return []Source{
		{
			// Talos v1.8.0 (Sep 2024) stopped attaching hypervisor images to
			// GitHub releases (Image Factory took over), and a VirtualBox OVA
			// was never a release asset at all — VirtualBox has no Talos
			// platform image anywhere, so it gets the metal ISO: boot into
			// maintenance mode, then apply config over the network.
			Name: "talos", Kind: KindGitHubAsset,
			Desc: "Talos Linux (Kubernetes node OS): metal ISO on VirtualBox, Image Factory OVA on VMware",
			Notes: "Image Factory publishes no checksum for the vmware OVA, so it " +
				"downloads unverified. It imports into Fusion with plain ovftool " +
				"(verified v1.13.8 / ovftool 5.0.0) and boots to maintenance mode, " +
				"but ovftool defaults its NIC to bridged — switch it to NAT when " +
				"host-side IP discovery via vmnet DHCP leases is wanted.",
			Variants: []Variant{
				{Family: "virtualbox", Locator: talosRepo,
					Asset: "metal-{arch}.iso", Format: provider.FormatISO},
				{Family: "vmware", Kind: KindTalosFactory, Locator: talosVanillaSchematic,
					Asset: "vmware-{arch}.ova", Format: provider.FormatOVA},
			},
		},
		{
			Name: "talos-iso", Kind: KindGitHubAsset,
			Desc: "Talos Linux official metal ISO (Sidero Labs)",
			Variants: []Variant{
				{Locator: talosRepo, Asset: "metal-{arch}.iso", Format: provider.FormatISO},
			},
		},
		{
			Name: "windows-dev", Kind: KindURL,
			Desc:  "Microsoft Windows development environment",
			Notes: "~20GB, evaluation-licensed, Microsoft publishes no checksum — downloaded unverified",
			Variants: []Variant{
				{Family: "virtualbox", Arch: "amd64",
					Locator: "https://aka.ms/windev_VM_virtualbox", Format: provider.FormatOVA},
				{Family: "vmware", Arch: "amd64",
					Locator: "https://aka.ms/windev_VM_vmware", Format: provider.FormatOVA},
			},
		},
		{
			Name: "ubuntu-cloud", Kind: KindUbuntuCloud, DefaultVersion: "24.04",
			Desc:  "Ubuntu Server cloud image (Canonical)",
			Notes: "cloud image: no default login — attach a NoCloud cloud-init seed ISO, or use the 'ubuntu' Vagrant entry instead",
			Variants: []Variant{
				{Format: provider.FormatOVA},
			},
		},
		{
			Name: "turnkey-core", Kind: KindURL,
			Desc:  "TurnKey Linux Core appliance OVA",
			Notes: "fixed version pin; no automated checksum",
			Variants: []Variant{
				{Arch: "amd64",
					Locator: "https://mirror.turnkeylinux.org/turnkeylinux/images/ova/turnkey-core-18.1-bookworm-amd64.ova",
					Format:  provider.FormatOVA},
			},
		},

		// Vagrant registry. One entry serves every provider: the box name is
		// shared and the provider name is supplied at resolution time.
		vagrant("ubuntu", "bento/ubuntu-24.04", "Ubuntu 24.04 LTS (bento)"),
		vagrant("debian", "bento/debian-12", "Debian 12 (bento)"),
		vagrant("fedora", "bento/fedora-41", "Fedora 41 (bento)"),
		vagrant("rocky", "bento/rockylinux-9", "Rocky Linux 9 (bento)"),
		vagrant("alma", "bento/almalinux-9", "AlmaLinux 9 (bento)"),
		vagrant("opensuse", "bento/opensuse-leap-15", "openSUSE Leap 15 (bento)"),
		vagrant("freebsd", "bento/freebsd-14", "FreeBSD 14 (bento)"),
		vagrant("kali", "kalilinux/rolling", "Kali Linux rolling"),
	}
}

func vagrant(name, box, desc string) Source {
	return Source{
		Name: name, Kind: KindVagrant, Desc: desc,
		Variants: []Variant{{Locator: box, Format: provider.FormatOVF}},
	}
}

// vagrantFormat is what a box actually contains once extracted. Boxes differ
// per provider in content, not just in disk format: "virtualbox" boxes carry
// box.ovf + disks, "vmware_desktop" boxes carry a .vmx bundle. VMware without
// ovftool still imports the latter, so labeling every box "ovf" would make
// Accepts reject artifacts the provider can consume.
func vagrantFormat(family string) provider.ImageFormat {
	if family == "vmware" {
		return provider.FormatVMX
	}
	return provider.FormatOVF
}

// ErrNoVariant reports that an image exists but has nothing usable for the
// given provider and architecture. It is deliberately distinct from "unknown
// image" so callers can tell a typo from a genuine capability gap.
type ErrNoVariant struct {
	Image, Family, Arch string
}

func (e *ErrNoVariant) Error() string {
	return fmt.Sprintf("image %q has no variant for %s/%s", e.Image, e.Family, e.Arch)
}

// Resolve selects the artifact for an image given a provider descriptor.
// Selection prefers an exact (family, arch) match, then family-only, then
// arch-only, then the unconstrained variant.
func Resolve(image string, d provider.Descriptor) (*Resolved, error) {
	arch := d.HostArch
	if arch == "" {
		arch = runtime.GOARCH
	}
	family := d.Kind.Family()

	if strings.HasPrefix(image, "vagrant:") {
		box := strings.TrimPrefix(image, "vagrant:")
		vp := d.Kind.VagrantProvider()
		if vp == "" {
			return nil, &ErrNoVariant{Image: image, Family: family, Arch: arch}
		}
		return &Resolved{
			Source: image, Kind: KindVagrant, Locator: box,
			Format: vagrantFormat(family), Arch: arch, VagrantProvider: vp,
		}, nil
	}

	var src *Source
	cat := Catalog()
	for i := range cat {
		if cat[i].Name == image {
			src = &cat[i]
			break
		}
	}
	if src == nil {
		return nil, fmt.Errorf("unknown image %q", image)
	}

	v := pick(src.Variants, family, arch)
	if v == nil {
		return nil, &ErrNoVariant{Image: image, Family: family, Arch: arch}
	}

	kind := src.Kind
	if v.Kind != "" {
		kind = v.Kind
	}
	r := &Resolved{
		Source:  src.Name,
		Kind:    kind,
		Locator: v.Locator,
		Asset:   strings.ReplaceAll(v.Asset, "{arch}", arch),
		Format:  v.Format,
		Arch:    arch,
		Notes:   src.Notes,
	}
	if src.Kind == KindVagrant {
		vp := d.Kind.VagrantProvider()
		if vp == "" {
			return nil, &ErrNoVariant{Image: image, Family: family, Arch: arch}
		}
		r.VagrantProvider = vp
		r.Format = vagrantFormat(family)
	}

	// A resolved artifact the provider cannot consume is a resolution failure,
	// not something to discover at import time.
	if !d.Accepts(r.Format) {
		return nil, fmt.Errorf(
			"image %q resolves to %s, which %s cannot import (accepted: %v)",
			image, r.Format, d.Kind, d.Formats)
	}
	return r, nil
}

// pick applies the specificity ladder: exact, family, arch, any.
func pick(vs []Variant, family, arch string) *Variant {
	var byFamily, byArch, any *Variant
	for i := range vs {
		v := &vs[i]
		switch {
		case v.Family == family && v.Arch == arch:
			return v
		case v.Family == family && v.Arch == "":
			if byFamily == nil {
				byFamily = v
			}
		case v.Family == "" && v.Arch == arch:
			if byArch == nil {
				byArch = v
			}
		case v.Family == "" && v.Arch == "":
			if any == nil {
				any = v
			}
		}
	}
	switch {
	case byFamily != nil:
		return byFamily
	case byArch != nil:
		return byArch
	default:
		return any
	}
}
