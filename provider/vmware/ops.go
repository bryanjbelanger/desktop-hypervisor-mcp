package vmware

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider"
)

// Ops implements the neutral operation surface over vmrun/ovftool. Ported
// from vmware-fusion-mcp-server v0.3 (which drove the Windows campaign),
// with verbs aligned to the contract and Workstation covered via HostType.
type Ops struct {
	desc provider.Descriptor
}

func New(d provider.Descriptor) *Ops { return &Ops{desc: d} }

func (o *Ops) Descriptor() provider.Descriptor { return o.desc }

// vmrun builds the invocation: -T <host type>, then credentials from the
// server environment (never from parameters), then the command.
func (o *Ops) vmrun(ctx context.Context, withGuest bool, args ...string) (string, error) {
	argv := []string{"-T", HostType()}
	if vp := os.Getenv("VMRUN_VM_PASSWORD"); vp != "" {
		argv = append(argv, "-vp", vp)
	}
	u, p := provider.GuestCreds(os.Getenv)
	if withGuest {
		if u == "" || p == "" {
			return "", fmt.Errorf("guest operations need HV_GUEST_USER and HV_GUEST_PASSWORD in the server environment — credentials are never tool parameters")
		}
		argv = append(argv, "-gu", u, "-gp", p)
	}
	return provider.RunCmd(ctx, VmrunBin(), append(argv, args...)...)
}

// resolveVMX turns a VM name or path into a .vmx path.
func (o *Ops) resolveVMX(vm string) (string, error) {
	if vm == "" {
		return "", fmt.Errorf("vm is required")
	}
	if strings.HasSuffix(vm, ".vmx") {
		return vm, nil
	}
	bundle := vm
	if !strings.HasSuffix(bundle, ".vmwarevm") {
		bundle = filepath.Join(o.desc.VMDir, vm+".vmwarevm")
	}
	if m, _ := filepath.Glob(filepath.Join(bundle, "*.vmx")); len(m) == 1 {
		return m[0], nil
	}
	if m, _ := filepath.Glob(filepath.Join(o.desc.VMDir, vm, "*.vmx")); len(m) == 1 {
		return m[0], nil
	}
	return "", fmt.Errorf("no .vmx found for %q under %s", vm, o.desc.VMDir)
}

func (o *Ops) ensureOff(ctx context.Context, vmx string) error {
	if running, _ := o.vmrun(ctx, false, "list"); strings.Contains(running, vmx) {
		return fmt.Errorf("VM is running — power it off first")
	}
	return nil
}

// setVMXKeys sets keys case-insensitively (VMX keys are case-insensitive; a
// case-mismatched duplicate makes the product reject the whole file — learned
// the hard way against bento's all-lowercase keys).
func setVMXKeys(vmx string, pairs [][2]string) error {
	data, err := os.ReadFile(vmx)
	if err != nil {
		return err
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	for _, kv := range pairs {
		newLine := fmt.Sprintf("%s = \"%s\"", kv[0], kv[1])
		replaced := false
		for i, l := range lines {
			if k, _, ok := strings.Cut(l, "="); ok && strings.EqualFold(strings.TrimSpace(k), kv[0]) {
				lines[i] = newLine
				replaced = true
				break
			}
		}
		if !replaced {
			lines = append(lines, newLine)
		}
	}
	return os.WriteFile(vmx, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

func vmxKey(vmx, key string) string {
	data, err := os.ReadFile(vmx)
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(data), "\n") {
		if k, v, ok := strings.Cut(l, "="); ok && strings.EqualFold(strings.TrimSpace(k), key) {
			return strings.Trim(strings.TrimSpace(v), `"`)
		}
	}
	return ""
}

// ------------------------------------------------------------------ inventory

func (o *Ops) List(ctx context.Context) ([]provider.VMRef, error) {
	running, _ := o.vmrun(ctx, false, "list")
	var vms []provider.VMRef
	seen := map[string]bool{}
	add := func(vmx string) {
		if vmx == "" || seen[vmx] {
			return
		}
		seen[vmx] = true
		name := strings.TrimSuffix(filepath.Base(filepath.Dir(vmx)), ".vmwarevm")
		state := "stopped"
		if strings.Contains(running, vmx) {
			state = "running"
		}
		vms = append(vms, provider.VMRef{Name: name, Path: vmx, State: state})
	}
	for _, pat := range []string{
		filepath.Join(o.desc.VMDir, "*.vmwarevm", "*.vmx"),
		filepath.Join(o.desc.VMDir, "*", "*.vmx"),
	} {
		m, _ := filepath.Glob(pat)
		for _, vmx := range m {
			add(vmx)
		}
	}
	return vms, nil
}

func (o *Ops) Running(ctx context.Context) ([]provider.VMRef, error) {
	out, err := o.vmrun(ctx, false, "list")
	if err != nil {
		return nil, err
	}
	var vms []provider.VMRef
	for _, l := range strings.Split(out, "\n") {
		if strings.HasSuffix(strings.TrimSpace(l), ".vmx") {
			vmx := strings.TrimSpace(l)
			name := strings.TrimSuffix(filepath.Base(filepath.Dir(vmx)), ".vmwarevm")
			vms = append(vms, provider.VMRef{Name: name, Path: vmx, State: "running"})
		}
	}
	return vms, nil
}

func (o *Ops) Show(ctx context.Context, vm string) (string, error) {
	vmx, err := o.resolveVMX(vm)
	if err != nil {
		return "", err
	}
	state := "stopped"
	if running, _ := o.vmrun(ctx, false, "list"); strings.Contains(running, vmx) {
		state = "running"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "vmx: %s\nstate: %s\n", vmx, state)
	for _, k := range []string{"displayname", "guestos", "numvcpus", "memsize",
		"firmware", "vhv.enable", "ethernet0.connectiontype", "ethernet0.generatedaddress"} {
		if v := vmxKey(vmx, k); v != "" {
			fmt.Fprintf(&b, "%s: %s\n", k, v)
		}
	}
	return strings.TrimSpace(b.String()), nil
}

// leaseFiles are where vmnet's DHCP daemon records leases, per platform.
func leaseFiles() []string {
	switch runtime.GOOS {
	case "darwin":
		return []string{"/var/db/vmware/vmnet-dhcpd-vmnet8.leases"}
	case "windows":
		return []string{`C:\ProgramData\VMware\vmnetdhcp.leases`}
	default:
		return []string{"/etc/vmware/vmnet8/dhcpd/dhcpd.leases", "/var/lib/vmware/vmnet8/dhcpd/dhcpd.leases"}
	}
}

var leaseBlockRe = regexp.MustCompile(`(?s)lease ([0-9.]+) \{(.*?)\}`)

func (o *Ops) IP(ctx context.Context, vm, network string) (string, error) {
	vmx, err := o.resolveVMX(vm)
	if err != nil {
		return "", err
	}
	// VMware Tools first (no -wait: a toolless guest would block forever).
	if out, err := o.vmrun(ctx, false, "getGuestIPAddress", vmx); err == nil {
		return strings.TrimSpace(out), nil
	}
	// vmnet DHCP lease by MAC — works for guests with no tools (Talos).
	mac := strings.ToLower(vmxKey(vmx, "ethernet0.generatedAddress"))
	if mac == "" {
		mac = strings.ToLower(vmxKey(vmx, "ethernet0.address"))
	}
	if mac == "" {
		return "", fmt.Errorf("no MAC recorded in %s (has the VM booted once?)", vmx)
	}
	for _, f := range leaseFiles() {
		data, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		ip := ""
		for _, m := range leaseBlockRe.FindAllStringSubmatch(string(data), -1) {
			if strings.Contains(strings.ToLower(m[2]), "hardware ethernet "+mac) {
				ip = m[1] // last match wins: the file appends renewals
			}
		}
		if ip != "" {
			return ip, nil
		}
	}
	return "", fmt.Errorf("no tools response and no DHCP lease for MAC %s (guest may still be booting)", mac)
}

// ------------------------------------------------------------------ lifecycle

func (o *Ops) Create(ctx context.Context, s provider.CreateSpec) (string, error) {
	if s.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	bundle := filepath.Join(o.desc.VMDir, s.Name+".vmwarevm")
	if _, err := os.Stat(bundle); err == nil {
		return "", fmt.Errorf("%s already exists", bundle)
	}
	if err := os.MkdirAll(bundle, 0o755); err != nil {
		return "", err
	}
	cpus, mem, disk := orInt(s.CPUs, 2), orInt(s.MemoryMB, 2048), orInt(s.DiskGB, 25)
	guestOS := s.GuestOS
	if guestOS == "" {
		guestOS = "other-64"
	}
	firmware := s.Firmware
	if firmware == "" {
		firmware = "efi"
	}

	vmdk := filepath.Join(bundle, s.Name+".vmdk")
	if vd := VdiskmanagerBin(); vd == "" {
		return "", fmt.Errorf("vmware-vdiskmanager not found — cannot create a disk")
	} else if _, err := provider.RunCmd(ctx, vd, "-c", "-s", fmt.Sprintf("%dGB", disk), "-a", "lsilogic", "-t", "0", vmdk); err != nil {
		return "", err
	}

	vmx := filepath.Join(bundle, s.Name+".vmx")
	// hpet0 is unconditional: its absence bluescreens Windows guests in early
	// timer calibration (found during the Server 2022 campaign) and is
	// harmless elsewhere.
	pairs := [][2]string{
		{".encoding", "UTF-8"}, {"config.version", "8"}, {"virtualHW.version", "19"},
		{"displayName", s.Name}, {"guestOS", guestOS},
		{"numvcpus", strconv.Itoa(cpus)}, {"cpuid.coresPerSocket", "1"},
		{"memsize", strconv.Itoa(mem)},
		{"firmware", firmware},
		{"hpet0.present", "TRUE"}, {"vmci0.present", "TRUE"},
		{"sata0.present", "TRUE"},
		{"sata0:0.present", "TRUE"}, {"sata0:0.fileName", s.Name + ".vmdk"},
		{"ethernet0.present", "TRUE"}, {"ethernet0.connectionType", "nat"},
		{"ethernet0.virtualDev", "e1000e"}, {"ethernet0.addressType", "generated"},
		{"usb.present", "TRUE"},
		{"tools.syncTime", "TRUE"},
		{"msg.autoanswer", "TRUE"},
	}
	if s.NestedVirt {
		pairs = append(pairs, [2]string{"vhv.enable", "TRUE"})
	}
	if err := setVMXKeys(vmx, pairs); err != nil {
		return "", err
	}
	return fmt.Sprintf("created %s: %d CPU, %dMB RAM, %dGB disk, %s firmware, guestOS %s\nvmx: %s",
		s.Name, cpus, mem, disk, firmware, guestOS, vmx), nil
}

func (o *Ops) Start(ctx context.Context, vm string, gui bool) (string, error) {
	vmx, err := o.resolveVMX(vm)
	if err != nil {
		return "", err
	}
	mode := "nogui"
	if gui {
		mode = "gui"
	}
	return o.vmrun(ctx, false, "start", vmx, mode)
}

func (o *Ops) Stop(ctx context.Context, vm string, hard bool) (string, error) {
	vmx, err := o.resolveVMX(vm)
	if err != nil {
		return "", err
	}
	mode := "soft"
	if hard {
		mode = "hard"
	}
	return o.vmrun(ctx, false, "stop", vmx, mode)
}

func (o *Ops) Delete(ctx context.Context, vm string) (string, error) {
	vmx, err := o.resolveVMX(vm)
	if err != nil {
		return "", err
	}
	return o.vmrun(ctx, false, "deleteVM", vmx)
}

func (o *Ops) Clone(ctx context.Context, s provider.CloneSpec) (string, error) {
	vmx, err := o.resolveVMX(s.Source)
	if err != nil {
		return "", err
	}
	destBundle := filepath.Join(o.desc.VMDir, s.Dest+".vmwarevm")
	if err := os.MkdirAll(destBundle, 0o755); err != nil {
		return "", err
	}
	destVMX := filepath.Join(destBundle, s.Dest+".vmx")
	mode := "full"
	if s.Linked {
		mode = "linked"
	}
	args := []string{"clone", vmx, destVMX, mode, "-cloneName=" + s.Dest}
	if s.Snapshot != "" {
		args = append(args, "-snapshot="+s.Snapshot)
	}
	out, err := o.vmrun(ctx, false, args...)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s\ncloned → %s (%s)", out, destVMX, mode), nil
}

func (o *Ops) ImportImage(ctx context.Context, path, name string) (string, error) {
	if strings.HasSuffix(path, ".vmx") {
		return "already consumable: " + path, nil
	}
	if !o.desc.Has(provider.CapOVAImport) {
		return "", provider.Unsupported(provider.CapOVAImport)
	}
	dest := o.desc.VMDir
	if name != "" {
		dest = filepath.Join(o.desc.VMDir, name+".vmwarevm")
	}
	// --lax --allowExtraConfig: cross-hypervisor OVAs carry descriptors ovftool
	// would otherwise reject outright.
	return provider.RunCmd(ctx, OvftoolBin(), "--lax", "--allowExtraConfig", path, dest)
}

func (o *Ops) ExportOVA(ctx context.Context, vm, dest string) (string, error) {
	if !o.desc.Has(provider.CapOVAExport) {
		return "", provider.Unsupported(provider.CapOVAExport)
	}
	vmx, err := o.resolveVMX(vm)
	if err != nil {
		return "", err
	}
	if err := o.ensureOff(ctx, vmx); err != nil {
		return "", err
	}
	return provider.RunCmd(ctx, OvftoolBin(), "--lax", vmx, dest)
}

// -------------------------------------------------------------- configuration

func (o *Ops) configure(ctx context.Context, vm string, pairs [][2]string) (string, error) {
	vmx, err := o.resolveVMX(vm)
	if err != nil {
		return "", err
	}
	if err := o.ensureOff(ctx, vmx); err != nil {
		return "", err
	}
	if err := setVMXKeys(vmx, pairs); err != nil {
		return "", err
	}
	var set []string
	for _, kv := range pairs {
		set = append(set, kv[0]+"="+kv[1])
	}
	return "set " + strings.Join(set, ", ") + " in " + vmx, nil
}

func (o *Ops) SetResources(ctx context.Context, vm string, cpus, memoryMB int) (string, error) {
	var pairs [][2]string
	if cpus > 0 {
		pairs = append(pairs, [2]string{"numvcpus", strconv.Itoa(cpus)})
	}
	if memoryMB > 0 {
		pairs = append(pairs, [2]string{"memsize", strconv.Itoa(memoryMB)})
	}
	if len(pairs) == 0 {
		return "", fmt.Errorf("nothing to change")
	}
	return o.configure(ctx, vm, pairs)
}

func (o *Ops) SetNestedVirt(ctx context.Context, vm string, enabled bool) (string, error) {
	v := "FALSE"
	if enabled {
		v = "TRUE"
	}
	return o.configure(ctx, vm, [][2]string{{"vhv.enable", v}})
}

func (o *Ops) AttachISO(ctx context.Context, vm, isoPath, slot string) (string, error) {
	if slot == "" {
		slot = "sata0:1"
	}
	if isoPath == "" {
		return o.configure(ctx, vm, [][2]string{{slot + ".present", "FALSE"}})
	}
	if _, err := os.Stat(isoPath); err != nil {
		return "", fmt.Errorf("iso not found: %s", isoPath)
	}
	return o.configure(ctx, vm, [][2]string{
		{slot + ".present", "TRUE"},
		{slot + ".deviceType", "cdrom-image"},
		{slot + ".fileName", isoPath},
		{slot + ".startConnected", "TRUE"},
	})
}

func (o *Ops) SetGuestinfo(ctx context.Context, vm string, kv map[string]string) (string, error) {
	var pairs [][2]string
	for k, v := range kv {
		pairs = append(pairs, [2]string{k, v})
	}
	if len(pairs) == 0 {
		return "", fmt.Errorf("no keys given")
	}
	return o.configure(ctx, vm, pairs)
}

// ---------------------------------------------------------------- snapshots

func (o *Ops) snap(ctx context.Context, vm string, args ...string) (string, error) {
	vmx, err := o.resolveVMX(vm)
	if err != nil {
		return "", err
	}
	return o.vmrun(ctx, false, append([]string{args[0], vmx}, args[1:]...)...)
}

func (o *Ops) SnapshotTake(ctx context.Context, vm, name string) (string, error) {
	return o.snap(ctx, vm, "snapshot", name)
}

func (o *Ops) SnapshotRestore(ctx context.Context, vm, name string) (string, error) {
	return o.snap(ctx, vm, "revertToSnapshot", name)
}

func (o *Ops) SnapshotDelete(ctx context.Context, vm, name string) (string, error) {
	return o.snap(ctx, vm, "deleteSnapshot", name)
}

func (o *Ops) SnapshotList(ctx context.Context, vm string) (string, error) {
	return o.snap(ctx, vm, "listSnapshots")
}

// -------------------------------------------------------------------- guest

func (o *Ops) GuestExec(ctx context.Context, vm, program string, args []string) (string, error) {
	vmx, err := o.resolveVMX(vm)
	if err != nil {
		return "", err
	}
	argv := append([]string{"runProgramInGuest", vmx, program}, args...)
	return o.vmrun(ctx, true, argv...)
}

func (o *Ops) GuestCopyIn(ctx context.Context, vm, hostSrc, guestDest string) (string, error) {
	vmx, err := o.resolveVMX(vm)
	if err != nil {
		return "", err
	}
	return o.vmrun(ctx, true, "CopyFileFromHostToGuest", vmx, hostSrc, guestDest)
}

func (o *Ops) GuestCopyOut(ctx context.Context, vm, guestSrc, hostDest string) (string, error) {
	vmx, err := o.resolveVMX(vm)
	if err != nil {
		return "", err
	}
	return o.vmrun(ctx, true, "CopyFileFromGuestToHost", vmx, guestSrc, hostDest)
}

func (o *Ops) CaptureScreen(ctx context.Context, vm, hostDest string) (string, error) {
	vmx, err := o.resolveVMX(vm)
	if err != nil {
		return "", err
	}
	out, err := o.vmrun(ctx, true, "captureScreen", vmx, hostDest)
	if err != nil {
		return "", err
	}
	if out == "(ok)" {
		out = "screenshot written to " + hostDest
	}
	return out, nil
}

// ------------------------------------------------------------------ network

// EnsureClusterNetwork: every VMware desktop install ships vmnet8, a shared
// NAT network with DHCP — the cluster intent is satisfied out of the box.
// Custom named NAT networks require product-specific privileged tooling, so
// a custom name is answered honestly rather than half-built.
func (o *Ops) EnsureClusterNetwork(ctx context.Context, name, cidr string) (string, error) {
	if name != "" && name != "vmnet8" && name != "cluster-net" {
		return "", fmt.Errorf("VMware desktop provides one shared NAT network (vmnet8); custom NAT networks need the product's own network editor (root). Attach VMs with connectionType nat and they share vmnet8")
	}
	subnet := ""
	if runtime.GOOS == "darwin" {
		if data, err := os.ReadFile("/Library/Preferences/VMware Fusion/networking"); err == nil {
			if m := regexp.MustCompile(`VNET_8_HOSTONLY_SUBNET ([0-9.]+)`).FindStringSubmatch(string(data)); m != nil {
				subnet = m[1] + "/24"
			}
		}
	}
	s := "vmnet8 (shared NAT with DHCP) is present on every VMware desktop install; VMs with connectionType nat share it"
	if subnet != "" {
		s += " — subnet " + subnet
	}
	return s, nil
}

func (o *Ops) ExposeGuestPort(ctx context.Context, network, proto, guestIP string, guestPort, hostPort int) (string, error) {
	if network == "" {
		network = "vmnet8"
	}
	if proto == "" {
		proto = "tcp"
	}
	out, err := o.vmrun(ctx, false, "setPortForwarding", network, proto,
		strconv.Itoa(hostPort), guestIP, strconv.Itoa(guestPort))
	if err != nil {
		return "", fmt.Errorf("%w (VMware port-forwarding changes require root — run the server privileged for this action, or forward manually)", err)
	}
	if out == "(ok)" {
		out = fmt.Sprintf("host 127.0.0.1:%d → %s:%d (%s on %s)", hostPort, guestIP, guestPort, proto, network)
	}
	return out, nil
}

// -------------------------------------------------------------------- misc

func (o *Ops) MakeISO(ctx context.Context, srcDir, dest, volumeLabel string) (string, error) {
	backend := isoBackend()
	if backend == "" {
		return "", provider.Unsupported(provider.CapMakeISO)
	}
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	var args []string
	switch filepath.Base(backend) {
	case "hdiutil":
		out := strings.TrimSuffix(dest, ".iso")
		args = []string{"makehybrid", "-iso", "-joliet", "-o", out}
		if volumeLabel != "" {
			args = append(args, "-default-volume-name", volumeLabel)
		}
		args = append(args, srcDir)
		res, err := provider.RunCmd(ctx, backend, args...)
		if err != nil {
			return "", err
		}
		return res + "\niso: " + out + ".iso", nil
	case "oscdimg.exe":
		args = []string{"-n", srcDir, dest}
	default: // xorriso / genisoimage / mkisofs
		args = []string{"-o", dest, "-J", "-r"}
		if volumeLabel != "" {
			args = append(args, "-V", volumeLabel)
		}
		if filepath.Base(backend) == "xorriso" {
			args = append([]string{"-as", "mkisofs"}, args...)
		}
		args = append(args, srcDir)
	}
	res, err := provider.RunCmd(ctx, backend, args...)
	if err != nil {
		return "", err
	}
	return res + "\niso: " + dest, nil
}

func (o *Ops) Raw(ctx context.Context, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("raw requires vmrun arguments")
	}
	// Guest credentials are included when configured so raw guest commands
	// work; they are omitted otherwise.
	u, p := provider.GuestCreds(os.Getenv)
	if u != "" && p != "" {
		return o.vmrun(ctx, true, args...)
	}
	return o.vmrun(ctx, false, args...)
}

func orInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
