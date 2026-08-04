package artifact

import (
	"errors"
	"testing"

	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider"
)

func desc(k provider.Kind, arch string, formats ...provider.ImageFormat) provider.Descriptor {
	if len(formats) == 0 {
		formats = []provider.ImageFormat{
			provider.FormatOVA, provider.FormatOVF, provider.FormatISO,
			provider.FormatVMX, provider.FormatVMDK, provider.FormatVDI,
		}
	}
	return provider.Descriptor{Kind: k, HostArch: arch, Formats: formats}
}

// The bug this package exists to fix: one image must resolve to a different
// artifact per hypervisor family.
func TestTalosResolvesPerFamily(t *testing.T) {
	vb, err := Resolve("talos", desc(provider.KindVirtualBox, "amd64"))
	if err != nil {
		t.Fatalf("virtualbox: %v", err)
	}
	vm, err := Resolve("talos", desc(provider.KindFusion, "amd64"))
	if err != nil {
		t.Fatalf("fusion: %v", err)
	}
	if vb.Asset == vm.Asset {
		t.Fatalf("talos resolved to the same asset for both families: %q", vb.Asset)
	}
	if vb.Asset != "virtualbox-amd64.ova" {
		t.Errorf("virtualbox asset = %q", vb.Asset)
	}
	if vm.Asset != "vmware-amd64.ova" {
		t.Errorf("vmware asset = %q", vm.Asset)
	}
}

// Workstation and Fusion share the VMware implementation, so they must
// resolve identically.
func TestFusionAndWorkstationAgree(t *testing.T) {
	f, err := Resolve("talos", desc(provider.KindFusion, "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	w, err := Resolve("talos", desc(provider.KindWorkstation, "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if *f != *w {
		t.Errorf("fusion %+v != workstation %+v", f, w)
	}
}

func TestArchSubstitution(t *testing.T) {
	r, err := Resolve("talos-iso", desc(provider.KindVirtualBox, "arm64"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Asset != "metal-arm64.iso" {
		t.Errorf("asset = %q, want metal-arm64.iso", r.Asset)
	}
}

// Vagrant boxes are published per provider; requesting the wrong one yields
// a box that exists but cannot be imported.
func TestVagrantProviderPerFamily(t *testing.T) {
	vb, err := Resolve("ubuntu", desc(provider.KindVirtualBox, "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if vb.VagrantProvider != "virtualbox" {
		t.Errorf("virtualbox box provider = %q", vb.VagrantProvider)
	}
	vm, err := Resolve("ubuntu", desc(provider.KindWorkstation, "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if vm.VagrantProvider != "vmware_desktop" {
		t.Errorf("vmware box provider = %q", vm.VagrantProvider)
	}
}

func TestVagrantPassthrough(t *testing.T) {
	r, err := Resolve("vagrant:bento/debian-12", desc(provider.KindFusion, "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Locator != "bento/debian-12" || r.VagrantProvider != "vmware_desktop" {
		t.Errorf("got %+v", r)
	}
}

// windows-dev is amd64-only; asking for it on arm64 is a capability gap, not
// a typo, and must be distinguishable as one.
func TestNoVariantIsDistinctFromUnknown(t *testing.T) {
	_, err := Resolve("windows-dev", desc(provider.KindFusion, "arm64"))
	var nv *ErrNoVariant
	if !errors.As(err, &nv) {
		t.Fatalf("want ErrNoVariant, got %v", err)
	}
	if _, err := Resolve("no-such-image", desc(provider.KindFusion, "amd64")); err == nil {
		t.Fatal("unknown image should error")
	} else if errors.As(err, &nv) {
		t.Fatal("unknown image must not report as a variant gap")
	}
}

// A provider without ovftool cannot accept OVA; resolution must fail there
// rather than at import time.
func TestFormatRejectedWhenProviderCannotImport(t *testing.T) {
	noOVA := desc(provider.KindFusion, "amd64",
		provider.FormatVMX, provider.FormatVMDK, provider.FormatISO)
	if _, err := Resolve("talos", noOVA); err == nil {
		t.Fatal("expected OVA rejection when ovftool is absent")
	}
	// The ISO path stays available on the same provider.
	if _, err := Resolve("talos-iso", noOVA); err != nil {
		t.Errorf("iso should still resolve: %v", err)
	}
}
