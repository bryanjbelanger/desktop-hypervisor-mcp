// Package virtualbox implements the provider contract for VirtualBox.
package virtualbox

import (
	"context"
	"os/exec"
	"regexp"
	"runtime"
	"strings"
	"time"

	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider"
)

// Bin resolves VBoxManage: PATH first, then the per-platform install location.
// PATH wins so a user's own install is always preferred.
func Bin() string {
	if p, err := exec.LookPath("VBoxManage"); err == nil {
		return p
	}
	switch runtime.GOOS {
	case "darwin":
		return "/usr/local/bin/VBoxManage"
	case "windows":
		return `C:\Program Files\Oracle\VirtualBox\VBoxManage.exe`
	default:
		return "/usr/bin/VBoxManage"
	}
}

type Detector struct{}

var machineFolderRe = regexp.MustCompile(`(?m)^Default machine folder:\s+(.*)$`)

func (Detector) Detect(ctx context.Context) []provider.Descriptor {
	d := provider.Descriptor{
		ID:               "virtualbox-local",
		Kind:             provider.KindVirtualBox,
		HostOS:           runtime.GOOS,
		HostArch:         runtime.GOARCH,
		StorageFreeBytes: -1,
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	out, err := exec.CommandContext(ctx, Bin(), "--version").Output()
	if err != nil {
		d.Status = provider.StatusMissingTooling
		d.Remediation = "VirtualBox is not installed or VBoxManage is not on PATH. " +
			"The system tool can self-install it from Oracle's official mirror."
		d.Capabilities = []provider.Capability{provider.CapSelfInstall}
		return []provider.Descriptor{d}
	}
	d.Status = provider.StatusReady
	d.Version = strings.TrimSpace(string(out))

	// VirtualBox virtualizes; it does not emulate a foreign architecture, so
	// guest arch is host arch.
	d.GuestArches = []string{runtime.GOARCH}

	d.Formats = []provider.ImageFormat{
		provider.FormatOVA, provider.FormatOVF, provider.FormatISO,
		provider.FormatVDI, provider.FormatVMDK,
	}
	d.NetworkModes = []string{"nat", "natnetwork", "hostonly", "bridged", "intnet"}

	d.Capabilities = []provider.Capability{
		provider.CapGuestExec, provider.CapGuestCopyIn, provider.CapGuestCopyOut,
		provider.CapOVAImport, provider.CapOVAExport,
		provider.CapLinkedClone, provider.CapPortForward, provider.CapSnapshotTree,
		provider.CapUEFI, provider.CapNestedVirt, provider.CapSelfInstall,
		// VirtualBox resolves guest IPs from its own DHCP server leases
		// (dhcpserver findlease), which needs no in-guest agent — the only
		// mechanism that works for Talos nodes.
		provider.CapIPFromDHCP,
		// Guest Additions expose the IP too, when a guest has them installed.
		provider.CapIPFromTools,
	}

	if props, err := exec.CommandContext(ctx, Bin(), "list", "systemproperties").Output(); err == nil {
		if m := machineFolderRe.FindStringSubmatch(string(props)); m != nil {
			d.VMDir = strings.TrimSpace(m[1])
			d.StorageFreeBytes = provider.FreeBytes(d.VMDir)
		}
	}
	return []provider.Descriptor{d}
}
