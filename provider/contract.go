// Package provider defines the contract every desktop hypervisor adapter
// implements. The contract is deliberately capability-based rather than
// uniform: adapters advertise what they can actually do, and callers branch
// on that instead of assuming a lowest common denominator.
//
// Two rules keep the abstraction honest:
//
//   - Abstract intent, never mechanism. "Ensure the cluster nodes share a
//     network and the host can reach port N" is expressible across providers.
//     "Create a host-only interface" is not — VirtualBox and VMware model
//     host networking differently, and pretending otherwise would lie about
//     what is happening on the host.
//   - Anything outside the neutral surface stays reachable through the
//     provider's raw escape hatch, unabstracted.
package provider

import "context"

// Kind identifies an adapter family. Fusion and Workstation are distinct
// user-facing providers but share the VMware implementation; they differ only
// in the vmrun -T argument, tool locations, and the host ISO backend.
type Kind string

const (
	KindVirtualBox  Kind = "virtualbox"
	KindFusion      Kind = "vmware-fusion"
	KindWorkstation Kind = "vmware-workstation"
)

// Family groups Kinds that share an implementation.
func (k Kind) Family() string {
	switch k {
	case KindFusion, KindWorkstation:
		return "vmware"
	default:
		return "virtualbox"
	}
}

// Status reports whether a provider can be used right now, and if not, why.
// A bare "not ready" is a dead end for the caller; every non-ready status
// carries a Remediation the skill can act on or offer to the user.
type Status string

const (
	StatusReady           Status = "ready"
	StatusMissingTooling  Status = "missing_tooling"   // hypervisor not installed
	StatusNeedsPermission Status = "needs_permission"  // installed, but elevation required
	StatusIncompatible    Status = "incompatible_arch" // present but cannot run the requested guest arch
)

// Capability names a discrete ability a provider may or may not have. The
// neutral tool surface always exists; capabilities decide whether a given
// action succeeds or returns ErrUnsupported.
type Capability string

const (
	CapGuestExec     Capability = "guest_exec"     // run a command inside a running guest
	CapGuestCopyIn   Capability = "guest_copy_in"  // host -> guest file transfer
	CapGuestCopyOut  Capability = "guest_copy_out" // guest -> host file transfer
	CapOVAImport     Capability = "ova_import"
	CapOVAExport     Capability = "ova_export"
	CapLinkedClone   Capability = "clone_linked"
	CapPortForward   Capability = "port_forward"
	CapSnapshotTree  Capability = "snapshot_tree" // hierarchical snapshots, not a flat list
	CapUEFI          Capability = "uefi"
	CapSecureBoot    Capability = "secure_boot"
	CapNestedVirt    Capability = "nested_virt"
	CapMakeISO       Capability = "make_iso"      // build an ISO on the host (kickstart/OEMDRV seeds)
	CapSelfInstall   Capability = "self_install"  // reserved: unimplemented, so no adapter may advertise it (PLAN.md item 9)
	CapIPFromDHCP    Capability = "ip_from_dhcp"  // resolve guest IP by MAC from hypervisor DHCP state
	CapIPFromTools   Capability = "ip_from_tools" // resolve guest IP via in-guest agent
	CapCaptureScreen Capability = "capture_screen"

	// CapGuestinfoConfig means the provider can inject configuration into a
	// guest before first boot, via hypervisor-level key/value pairs the guest
	// reads at startup (VMware guestinfo.* in the .vmx). Providers without it
	// must deliver configuration over the network after the guest has booted
	// into some maintenance mode, which requires discovering its IP first.
	// This inverts the bring-up sequence, so it is a contract member rather
	// than an implementation detail.
	CapGuestinfoConfig Capability = "guestinfo_config"
)

// ImageFormat is an artifact container a provider can consume directly.
type ImageFormat string

const (
	FormatOVA  ImageFormat = "ova"
	FormatOVF  ImageFormat = "ovf"
	FormatISO  ImageFormat = "iso"
	FormatVMX  ImageFormat = "vmx"
	FormatVDI  ImageFormat = "vdi"
	FormatVMDK ImageFormat = "vmdk"
)

// VagrantProvider is the name this hypervisor uses in the Vagrant box
// registry. Boxes are published per-provider, so the artifact resolver must
// ask for the right one — a "virtualbox" box is not usable by VMware.
// Empty means the provider cannot consume Vagrant boxes at all.
func (k Kind) VagrantProvider() string {
	switch k {
	case KindVirtualBox:
		return "virtualbox"
	case KindFusion, KindWorkstation:
		return "vmware_desktop"
	}
	return ""
}

// Descriptor is what a provider advertises to callers. It answers every
// question the stand-up-talos skill needs before it commits to a provider:
// can it run here, what can it run, and what does it accept.
type Descriptor struct {
	ID     string `json:"id"`   // stable instance id, e.g. "fusion-local"
	Kind   Kind   `json:"type"` // adapter kind
	Status Status `json:"status"`

	// Remediation is set whenever Status != StatusReady: a one-line, actionable
	// description of what would make this provider usable.
	Remediation string `json:"remediation,omitempty"`

	HostOS   string `json:"host_os"`   // runtime.GOOS
	HostArch string `json:"host_arch"` // runtime.GOARCH

	Version string `json:"version,omitempty"` // hypervisor version, when detectable

	GuestArches  []string      `json:"guest_arches"`  // architectures this host can run
	Formats      []ImageFormat `json:"formats"`       // artifact formats accepted directly
	NetworkModes []string      `json:"network_modes"` // provider-native mode names, informational
	Capabilities []Capability  `json:"capabilities"`

	// StorageFreeBytes is available space on the VM store, or -1 if unknown.
	StorageFreeBytes int64  `json:"storage_free_bytes"`
	VMDir            string `json:"vm_dir,omitempty"`
}

// Has reports whether the descriptor advertises a capability.
func (d Descriptor) Has(c Capability) bool {
	for _, x := range d.Capabilities {
		if x == c {
			return true
		}
	}
	return false
}

// Accepts reports whether the provider can consume an artifact format directly.
func (d Descriptor) Accepts(f ImageFormat) bool {
	for _, x := range d.Formats {
		if x == f {
			return true
		}
	}
	return false
}

// Detector probes the host for one adapter family and reports what it found.
// Detection must never fail the server start: an absent hypervisor is a
// non-ready Descriptor, not an error.
type Detector interface {
	Detect(ctx context.Context) []Descriptor
}
