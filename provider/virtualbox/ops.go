package virtualbox

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider"
)

// Ops implements the neutral operation surface over VBoxManage. Ported from
// virtualbox-mcp-server v2.5 (campaign-proven on macOS/Debian/Fedora), with
// verbs and shapes aligned to the provider contract.
type Ops struct {
	desc provider.Descriptor
}

func New(d provider.Descriptor) *Ops { return &Ops{desc: d} }

func (o *Ops) Descriptor() provider.Descriptor { return o.desc }

func (o *Ops) run(ctx context.Context, args ...string) (string, error) {
	return provider.RunCmd(ctx, Bin(), args...)
}

// ------------------------------------------------------------------ inventory

var vmLineRe = regexp.MustCompile(`"([^"]*)" \{([0-9a-f-]+)\}`)

func (o *Ops) parseVMs(out string, state string) []provider.VMRef {
	var vms []provider.VMRef
	for _, m := range vmLineRe.FindAllStringSubmatch(out, -1) {
		vms = append(vms, provider.VMRef{Name: m[1], Path: m[2], State: state})
	}
	return vms
}

func (o *Ops) List(ctx context.Context) ([]provider.VMRef, error) {
	all, err := o.run(ctx, "list", "vms")
	if err != nil {
		return nil, err
	}
	running, err := o.run(ctx, "list", "runningvms")
	if err != nil {
		return nil, err
	}
	up := map[string]bool{}
	for _, vm := range o.parseVMs(running, "running") {
		up[vm.Name] = true
	}
	vms := o.parseVMs(all, "stopped")
	for i := range vms {
		if up[vms[i].Name] {
			vms[i].State = "running"
		}
	}
	return vms, nil
}

func (o *Ops) Running(ctx context.Context) ([]provider.VMRef, error) {
	out, err := o.run(ctx, "list", "runningvms")
	if err != nil {
		return nil, err
	}
	return o.parseVMs(out, "running"), nil
}

func (o *Ops) Show(ctx context.Context, vm string) (string, error) {
	return o.run(ctx, "showvminfo", vm)
}

var (
	macRe   = regexp.MustCompile(`macaddress1="([0-9A-Fa-f]+)"`)
	valueRe = regexp.MustCompile(`Value: (\S+)`)
	leaseRe = regexp.MustCompile(`IP Address:\s+(\S+)`)
)

func (o *Ops) IP(ctx context.Context, vm, network string) (string, error) {
	// Guest Additions first: instant and exact when present.
	if out, err := o.run(ctx, "guestproperty", "get", vm, "/VirtualBox/GuestInfo/Net/0/V4/IP"); err == nil {
		if m := valueRe.FindStringSubmatch(out); m != nil {
			return m[1], nil
		}
	}
	// DHCP lease by MAC — the mechanism that needs no in-guest agent.
	info, err := o.run(ctx, "showvminfo", vm, "--machinereadable")
	if err != nil {
		return "", err
	}
	m := macRe.FindStringSubmatch(info)
	if m == nil {
		return "", fmt.Errorf("no NIC MAC found for %s", vm)
	}
	if network == "" {
		// Try every NAT network rather than demanding the caller know one.
		nets, _ := o.run(ctx, "natnetwork", "list")
		for _, nm := range regexp.MustCompile(`(?m)^Name:\s+(\S+)`).FindAllStringSubmatch(nets, -1) {
			if out, err := o.run(ctx, "dhcpserver", "findlease", "--network="+nm[1], "--mac-address="+m[1]); err == nil {
				if ip := leaseRe.FindStringSubmatch(out); ip != nil {
					return ip[1], nil
				}
			}
		}
		return "", fmt.Errorf("no lease found for %s on any NAT network (guest may still be booting)", vm)
	}
	out, err := o.run(ctx, "dhcpserver", "findlease", "--network="+network, "--mac-address="+m[1])
	if err != nil {
		return "", err
	}
	if ip := leaseRe.FindStringSubmatch(out); ip != nil {
		return ip[1], nil
	}
	return "", fmt.Errorf("no lease for %s on %s", vm, network)
}

// ------------------------------------------------------------------ lifecycle

func (o *Ops) Create(ctx context.Context, s provider.CreateSpec) (string, error) {
	if s.Name == "" {
		return "", fmt.Errorf("name is required")
	}
	osType := s.GuestOS
	if osType == "" {
		osType = "Other_64"
	}
	if _, err := o.run(ctx, "createvm", "--name", s.Name, "--ostype", osType, "--register"); err != nil {
		return "", err
	}
	cpus, mem := orInt(s.CPUs, 2), orInt(s.MemoryMB, 2048)
	modify := []string{"modifyvm", s.Name,
		"--cpus", strconv.Itoa(cpus), "--memory", strconv.Itoa(mem), "--vram", "16"}
	if s.Firmware == "efi" {
		modify = append(modify, "--firmware", "efi")
	}
	if s.NestedVirt {
		modify = append(modify, "--nested-hw-virt", "on")
	}
	if _, err := o.run(ctx, modify...); err != nil {
		return "", err
	}
	summary := fmt.Sprintf("created %s: %d CPU, %dMB RAM, ostype %s", s.Name, cpus, mem, osType)
	if s.DiskGB > 0 {
		if _, err := o.run(ctx, "storagectl", s.Name, "--name", "SATA", "--add", "sata", "--controller", "IntelAhci"); err != nil {
			return "", err
		}
		disk := filepath.Join(o.desc.VMDir, s.Name, s.Name+".vdi")
		if _, err := o.run(ctx, "createmedium", "disk", "--filename", disk,
			"--size", strconv.Itoa(s.DiskGB*1024)); err != nil {
			return "", err
		}
		if _, err := o.run(ctx, "storageattach", s.Name, "--storagectl", "SATA",
			"--port", "0", "--device", "0", "--type", "hdd", "--medium", disk); err != nil {
			return "", err
		}
		summary += fmt.Sprintf(", %dGB disk on SATA:0", s.DiskGB)
	}
	return summary, nil
}

func (o *Ops) Start(ctx context.Context, vm string, gui bool) (string, error) {
	mode := "headless"
	if gui {
		mode = "gui"
	}
	return o.run(ctx, "startvm", vm, "--type", mode)
}

func (o *Ops) Stop(ctx context.Context, vm string, hard bool) (string, error) {
	sub := "acpipowerbutton"
	if hard {
		sub = "poweroff"
	}
	return o.run(ctx, "controlvm", vm, sub)
}

func (o *Ops) Delete(ctx context.Context, vm string) (string, error) {
	return o.run(ctx, "unregistervm", vm, "--delete")
}

func (o *Ops) Clone(ctx context.Context, s provider.CloneSpec) (string, error) {
	args := []string{"clonevm", s.Source, "--name", s.Dest, "--register"}
	if s.Snapshot != "" {
		args = append(args, "--snapshot", s.Snapshot)
	}
	if s.Linked {
		if s.Snapshot == "" {
			return "", fmt.Errorf("VirtualBox linked clones require a snapshot to link from")
		}
		args = append(args, "--options", "link")
	}
	return o.run(ctx, args...)
}

func (o *Ops) ImportImage(ctx context.Context, path, name string) (string, error) {
	args := []string{"import", path, "--vsys", "0"}
	if name != "" {
		args = append(args, "--vmname", name)
	}
	return o.run(ctx, args...)
}

func (o *Ops) ExportOVA(ctx context.Context, vm, dest string) (string, error) {
	return o.run(ctx, "export", vm, "-o", dest)
}

// -------------------------------------------------------------- configuration

func (o *Ops) SetResources(ctx context.Context, vm string, cpus, memoryMB int) (string, error) {
	args := []string{"modifyvm", vm}
	if cpus > 0 {
		args = append(args, "--cpus", strconv.Itoa(cpus))
	}
	if memoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(memoryMB))
	}
	if len(args) == 2 {
		return "", fmt.Errorf("nothing to change")
	}
	return o.run(ctx, args...)
}

func (o *Ops) SetNestedVirt(ctx context.Context, vm string, enabled bool) (string, error) {
	v := "off"
	if enabled {
		v = "on"
	}
	return o.run(ctx, "modifyvm", vm, "--nested-hw-virt", v)
}

func (o *Ops) AttachISO(ctx context.Context, vm, isoPath, slot string) (string, error) {
	ctl, port := "SATA", "1"
	if slot != "" {
		if c, p, ok := strings.Cut(slot, ":"); ok {
			ctl, port = c, p
		} else {
			return "", fmt.Errorf("slot must be CONTROLLER:PORT, e.g. SATA:1")
		}
	}
	medium := isoPath
	if medium == "" {
		medium = "emptydrive"
	} else if _, err := os.Stat(medium); err != nil {
		return "", fmt.Errorf("iso not found: %s", medium)
	}
	return o.run(ctx, "storageattach", vm, "--storagectl", ctl, "--port", port,
		"--device", "0", "--type", "dvddrive", "--medium", medium)
}

func (o *Ops) SetGuestinfo(ctx context.Context, vm string, kv map[string]string) (string, error) {
	return "", provider.Unsupported(provider.CapGuestinfoConfig)
}

// ---------------------------------------------------------------- snapshots

func (o *Ops) SnapshotTake(ctx context.Context, vm, name string) (string, error) {
	return o.run(ctx, "snapshot", vm, "take", name)
}

func (o *Ops) SnapshotRestore(ctx context.Context, vm, name string) (string, error) {
	if name == "" {
		return o.run(ctx, "snapshot", vm, "restorecurrent")
	}
	return o.run(ctx, "snapshot", vm, "restore", name)
}

func (o *Ops) SnapshotDelete(ctx context.Context, vm, name string) (string, error) {
	return o.run(ctx, "snapshot", vm, "delete", name)
}

func (o *Ops) SnapshotList(ctx context.Context, vm string) (string, error) {
	return o.run(ctx, "snapshot", vm, "list")
}

// -------------------------------------------------------------------- guest

func (o *Ops) creds() (string, string, error) {
	u, p := provider.GuestCreds(os.Getenv)
	if u == "" || p == "" {
		return "", "", fmt.Errorf("guest operations need HV_GUEST_USER and HV_GUEST_PASSWORD in the server environment — credentials are never tool parameters")
	}
	return u, p, nil
}

func (o *Ops) GuestExec(ctx context.Context, vm, program string, args []string) (string, error) {
	u, p, err := o.creds()
	if err != nil {
		return "", err
	}
	argv := []string{"guestcontrol", vm, "run", "--username", u, "--password", p,
		"--wait-stdout", "--wait-stderr", "--exe", program, "--"}
	argv = append(argv, program)
	argv = append(argv, args...)
	return o.run(ctx, argv...)
}

func (o *Ops) GuestCopyIn(ctx context.Context, vm, hostSrc, guestDest string) (string, error) {
	u, p, err := o.creds()
	if err != nil {
		return "", err
	}
	return o.run(ctx, "guestcontrol", vm, "copyto", "--username", u, "--password", p, hostSrc, guestDest)
}

func (o *Ops) GuestCopyOut(ctx context.Context, vm, guestSrc, hostDest string) (string, error) {
	u, p, err := o.creds()
	if err != nil {
		return "", err
	}
	return o.run(ctx, "guestcontrol", vm, "copyfrom", "--username", u, "--password", p, guestSrc, hostDest)
}

func (o *Ops) CaptureScreen(ctx context.Context, vm, hostDest string) (string, error) {
	out, err := o.run(ctx, "controlvm", vm, "screenshotpng", hostDest)
	if err != nil {
		return "", err
	}
	if out == "(ok)" {
		out = "screenshot written to " + hostDest
	}
	return out, nil
}

// ------------------------------------------------------------------ network

func (o *Ops) EnsureClusterNetwork(ctx context.Context, name, cidr string) (string, error) {
	if name == "" {
		name = "cluster-net"
	}
	if cidr == "" {
		cidr = "192.168.100.0/24"
	}
	if out, _ := o.run(ctx, "natnetwork", "list"); regexp.MustCompile(`(?m)^Name:\s+` + regexp.QuoteMeta(name) + `$`).MatchString(out) {
		return fmt.Sprintf("network %s already exists (NAT network; attach VMs with --nic1 natnetwork --nat-network1 %s)", name, name), nil
	}
	if _, err := o.run(ctx, "natnetwork", "add", "--netname", name, "--network", cidr,
		"--enable", "--dhcp", "on"); err != nil {
		return "", err
	}
	return fmt.Sprintf("created NAT network %s (%s, DHCP on); attach VMs with --nic1 natnetwork --nat-network1 %s", name, cidr, name), nil
}

func (o *Ops) ExposeGuestPort(ctx context.Context, network, proto, guestIP string, guestPort, hostPort int) (string, error) {
	if network == "" {
		network = "cluster-net"
	}
	if proto == "" {
		proto = "tcp"
	}
	rule := fmt.Sprintf("pf-%d-%d:%s:[]:%d:[%s]:%d", hostPort, guestPort, proto, hostPort, guestIP, guestPort)
	if _, err := o.run(ctx, "natnetwork", "modify", "--netname", network, "--port-forward-4", rule); err != nil {
		return "", err
	}
	return fmt.Sprintf("host 127.0.0.1:%d → %s:%d (%s on %s)", hostPort, guestIP, guestPort, proto, network), nil
}

// -------------------------------------------------------------------- misc

func (o *Ops) MakeISO(ctx context.Context, srcDir, dest, volumeLabel string) (string, error) {
	return "", provider.Unsupported(provider.CapMakeISO)
}

func (o *Ops) Raw(ctx context.Context, args []string) (string, error) {
	if len(args) == 0 {
		return "", fmt.Errorf("raw requires VBoxManage arguments")
	}
	return o.run(ctx, args...)
}

func orInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}
