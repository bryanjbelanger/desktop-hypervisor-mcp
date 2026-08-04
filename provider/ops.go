package provider

import (
	"context"
	"errors"
	"fmt"
)

// ErrUnsupported is returned by an Ops method the provider cannot perform.
// Callers gate on Descriptor capabilities first; this is the backstop.
var ErrUnsupported = errors.New("not supported by this provider")

// Unsupported wraps ErrUnsupported with the capability that is missing, so
// the caller's error names what to check rather than a bare refusal.
func Unsupported(c Capability) error {
	return fmt.Errorf("%w (capability %s not advertised)", ErrUnsupported, c)
}

// VMRef identifies a VM to the caller. Name is the user-facing handle every
// Ops method accepts; Path is the provider-native locator (config file or
// registry entry) and is informational.
type VMRef struct {
	Name  string `json:"name"`
	Path  string `json:"path,omitempty"`
	State string `json:"state,omitempty"` // running|stopped|suspended|unknown
}

// CreateSpec describes a new empty VM.
type CreateSpec struct {
	Name       string
	CPUs       int    // default 2
	MemoryMB   int    // default 2048
	DiskGB     int    // 0 = no disk
	GuestOS    string // provider-native guest OS id; empty = provider default
	Firmware   string // "efi" | "bios" | "" (provider default)
	NestedVirt bool
}

// CloneSpec describes a clone operation.
type CloneSpec struct {
	Source   string
	Dest     string
	Snapshot string // clone from this snapshot when set
	Linked   bool   // requires CapLinkedClone
}

// Ops is the neutral operation surface. Every adapter implements all of it;
// methods a provider cannot honor return ErrUnsupported. The two contract
// rules apply throughout: methods express intent (EnsureClusterNetwork), and
// provider mechanism stays reachable only through Raw.
type Ops interface {
	Descriptor() Descriptor

	// Inventory and inspection.
	List(ctx context.Context) ([]VMRef, error)
	Running(ctx context.Context) ([]VMRef, error)
	Show(ctx context.Context, vm string) (string, error)
	// IP resolves a guest address: in-guest tools when available, otherwise
	// the hypervisor's DHCP state by MAC (network names the provider network
	// to search where that matters; empty = provider default).
	IP(ctx context.Context, vm, network string) (string, error)

	// Lifecycle.
	Create(ctx context.Context, spec CreateSpec) (string, error)
	Start(ctx context.Context, vm string, gui bool) (string, error)
	Stop(ctx context.Context, vm string, hard bool) (string, error)
	// Suspend saves state to disk; Start resumes. Reset is a reboot (hard
	// skips the guest-OS shutdown path). Both exist natively on all providers.
	Suspend(ctx context.Context, vm string) (string, error)
	Reset(ctx context.Context, vm string, hard bool) (string, error)
	Delete(ctx context.Context, vm string) (string, error)
	Clone(ctx context.Context, spec CloneSpec) (string, error)
	ImportImage(ctx context.Context, path, name string) (string, error)
	ExportOVA(ctx context.Context, vm, dest string) (string, error)

	// Configuration (VM powered off where the provider requires it).
	SetResources(ctx context.Context, vm string, cpus, memoryMB int) (string, error)
	SetNestedVirt(ctx context.Context, vm string, enabled bool) (string, error)
	// AttachISO connects an ISO (empty path detaches). Slot is provider-native
	// ("SATA:1" on VirtualBox, "sata0:1" on VMware); empty picks a default.
	AttachISO(ctx context.Context, vm, isoPath, slot string) (string, error)
	// SetGuestinfo injects pre-boot key/value config the guest reads at
	// startup (CapGuestinfoConfig). Keys are passed as-is.
	SetGuestinfo(ctx context.Context, vm string, kv map[string]string) (string, error)

	// Snapshots. Verb is "restore" everywhere. tree asks for the hierarchy
	// (CapSnapshotTree); children cascades a delete where the provider can.
	SnapshotTake(ctx context.Context, vm, name string) (string, error)
	SnapshotRestore(ctx context.Context, vm, name string) (string, error)
	SnapshotDelete(ctx context.Context, vm, name string, children bool) (string, error)
	SnapshotList(ctx context.Context, vm string, tree bool) (string, error)

	// Guest operations (CapGuestExec / CapGuestCopy*). Credentials come from
	// the server process environment (HV_GUEST_USER / HV_GUEST_PASSWORD),
	// never from tool parameters.
	GuestExec(ctx context.Context, vm, program string, args []string) (string, error)
	// GuestScript feeds script text to an interpreter in the guest — the
	// workhorse for one-liners (bash, powershell.exe) where GuestExec's
	// absolute-program-plus-args shape is too rigid.
	GuestScript(ctx context.Context, vm, interpreter, script string) (string, error)
	GuestCopyIn(ctx context.Context, vm, hostSrc, guestDest string) (string, error)
	GuestCopyOut(ctx context.Context, vm, guestSrc, hostDest string) (string, error)
	CaptureScreen(ctx context.Context, vm, hostDest string) (string, error)

	// Networking by intent only.
	EnsureClusterNetwork(ctx context.Context, name, cidr string) (string, error)
	ExposeGuestPort(ctx context.Context, network, proto, guestIP string, guestPort, hostPort int) (string, error)

	// MakeISO builds an ISO from a host directory (CapMakeISO) — the seed
	// mechanism for kickstart/autounattend flows.
	MakeISO(ctx context.Context, srcDir, dest, volumeLabel string) (string, error)

	// Raw is the provider-native escape hatch: the full argv after the
	// provider CLI's name (VBoxManage … / vmrun …).
	Raw(ctx context.Context, args []string) (string, error)
}

// GuestCreds reads guest credentials from the environment. The HV_* names are
// canonical; the VMRUN_* names are honored for compatibility with the
// predecessor Fusion server's registrations.
func GuestCreds(get func(string) string) (user, pass string) {
	user = get("HV_GUEST_USER")
	if user == "" {
		user = get("VMRUN_GUEST_USER")
	}
	pass = get("HV_GUEST_PASSWORD")
	if pass == "" {
		pass = get("VMRUN_GUEST_PASSWORD")
	}
	return user, pass
}
