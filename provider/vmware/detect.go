// Package vmware implements the provider contract for the VMware desktop
// hypervisors. Fusion (macOS) and Workstation (Windows/Linux) share vmrun,
// ovftool, vmware-vdiskmanager and the .vmx format; they differ only in the
// vmrun -T argument, tool locations, and the host ISO backend.
package vmware

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"time"

	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider"
)

// Kind is Fusion on macOS and Workstation everywhere else. The two products
// are mutually exclusive per platform, so GOOS decides it outright — no
// probing required.
func Kind() provider.Kind {
	if runtime.GOOS == "darwin" {
		return provider.KindFusion
	}
	return provider.KindWorkstation
}

// HostType is the vmrun -T argument for this platform.
func HostType() string {
	if runtime.GOOS == "darwin" {
		return "fusion"
	}
	return "ws"
}

// lookup resolves a VMware tool: PATH first, then the per-platform fallbacks.
// Returns "" when the tool cannot be found anywhere.
func lookup(name string, fallbacks ...string) string {
	if p, err := exec.LookPath(name); err == nil {
		return p
	}
	for _, f := range fallbacks {
		if f == "" {
			continue
		}
		if st, err := exec.LookPath(f); err == nil {
			return st
		}
	}
	return ""
}

const fusionApp = "/Applications/VMware Fusion.app/Contents"

// VmrunBin resolves vmrun for this platform.
func VmrunBin() string {
	switch runtime.GOOS {
	case "darwin":
		return lookup("vmrun", fusionApp+"/Public/vmrun")
	case "windows":
		return lookup("vmrun.exe", `C:\Program Files (x86)\VMware\VMware Workstation\vmrun.exe`)
	default:
		return lookup("vmrun", "/usr/bin/vmrun")
	}
}

// OvftoolBin resolves ovftool, which ships with the hypervisor but is often
// not on PATH. Its absence removes OVA import/export rather than failing.
func OvftoolBin() string {
	switch runtime.GOOS {
	case "darwin":
		return lookup("ovftool", fusionApp+"/Library/VMware OVF Tool/ovftool")
	case "windows":
		return lookup("ovftool.exe", `C:\Program Files (x86)\VMware\VMware Workstation\OVFTool\ovftool.exe`)
	default:
		return lookup("ovftool", "/usr/bin/ovftool", "/usr/lib/vmware-ovftool/ovftool")
	}
}

// VdiskmanagerBin resolves vmware-vdiskmanager, needed to create disks —
// vmrun cannot create a VM.
func VdiskmanagerBin() string {
	switch runtime.GOOS {
	case "darwin":
		return lookup("vmware-vdiskmanager", fusionApp+"/Library/vmware-vdiskmanager")
	case "windows":
		return lookup("vmware-vdiskmanager.exe",
			`C:\Program Files (x86)\VMware\VMware Workstation\vmware-vdiskmanager.exe`)
	default:
		return lookup("vmware-vdiskmanager", "/usr/bin/vmware-vdiskmanager")
	}
}

// isoBackend returns the host tool used to build an ISO, or "" when none is
// available. hdiutil is macOS-only; Linux and Windows need a separate tool,
// and on Windows oscdimg ships with the Windows ADK rather than the OS.
func isoBackend() string {
	switch runtime.GOOS {
	case "darwin":
		return lookup("hdiutil", "/usr/bin/hdiutil")
	case "windows":
		return lookup("oscdimg.exe")
	default:
		return lookup("xorriso", "genisoimage", "mkisofs")
	}
}

type Detector struct{}

var versionRe = regexp.MustCompile(`(?i)vmrun version ([0-9][0-9.]*)`)

func (Detector) Detect(ctx context.Context) []provider.Descriptor {
	kind := Kind()
	d := provider.Descriptor{
		ID:               string(kind) + "-local",
		Kind:             kind,
		HostOS:           runtime.GOOS,
		HostArch:         runtime.GOARCH,
		StorageFreeBytes: -1,
	}

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	vmrun := VmrunBin()
	if vmrun == "" {
		d.Status = provider.StatusMissingTooling
		name := "VMware Workstation"
		if kind == provider.KindFusion {
			name = "VMware Fusion"
		}
		d.Remediation = name + " is not installed (vmrun not found). " +
			"Broadcom distributes it free for personal and commercial use."
		return []provider.Descriptor{d}
	}
	d.Status = provider.StatusReady

	// vmrun with no arguments prints a usage banner beginning with its version.
	// It exits non-zero doing so, hence CombinedOutput and the ignored error.
	out, _ := exec.CommandContext(ctx, vmrun).CombinedOutput()
	if m := versionRe.FindStringSubmatch(string(out)); m != nil {
		d.Version = m[1]
	}

	d.GuestArches = []string{runtime.GOARCH}
	d.NetworkModes = []string{"nat", "hostOnly", "bridged", "custom"}

	d.Formats = []provider.ImageFormat{
		provider.FormatVMX, provider.FormatVMDK, provider.FormatISO,
	}

	caps := []provider.Capability{
		// VMware Tools drive guest exec and both copy directions.
		provider.CapGuestExec, provider.CapGuestCopyIn, provider.CapGuestCopyOut,
		provider.CapLinkedClone, provider.CapSnapshotTree,
		provider.CapUEFI, provider.CapNestedVirt, provider.CapCaptureScreen,
		provider.CapIPFromTools,
		// vmnet writes DHCP leases the host can read, so node IPs resolve by
		// MAC without any in-guest agent. This is the mechanism that works for
		// Talos, which ships no VMware Tools.
		provider.CapIPFromDHCP,
		// Port forwarding edits the vmnet NAT config, which needs elevation.
		provider.CapPortForward,
		// Arbitrary .vmx keys can be set before boot, which is how Talos
		// receives its machine config on VMware: guestinfo.talos.config set
		// to the base64 of controlplane.yaml. No network round-trip, no
		// maintenance mode, no IP needed before the node configures itself.
		provider.CapGuestinfoConfig,
	}

	// OVA support is conditional: ovftool ships with the hypervisor but is
	// frequently absent from PATH, and on Linux is a separate download.
	if OvftoolBin() != "" {
		caps = append(caps, provider.CapOVAImport, provider.CapOVAExport)
		d.Formats = append(d.Formats, provider.FormatOVA, provider.FormatOVF)
	}

	// ISO authoring backs the kickstart/OEMDRV seed flow and has no portable
	// implementation; advertise it only when a backend is actually present.
	if isoBackend() != "" {
		caps = append(caps, provider.CapMakeISO)
	}

	d.Capabilities = caps
	d.VMDir = defaultVMDir()
	d.StorageFreeBytes = provider.FreeBytes(d.VMDir)
	return []provider.Descriptor{d}
}

// defaultVMDir is where the product stores VMs unless the user moved it.
// vmrun exposes no query for this, so it is derived from the documented
// per-platform default.
func defaultVMDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Virtual Machines.localized")
	case "windows":
		return filepath.Join(home, "Documents", "Virtual Machines")
	default:
		return filepath.Join(home, "vmware")
	}
}
