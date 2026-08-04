// Desktop Hypervisor MCP — one provider-neutral server over VirtualBox,
// VMware Fusion, and VMware Workstation.
//
// The neutral tool surface expresses intent; provider mechanism stays behind
// execute_command. Capabilities decide what succeeds — see provider/contract.go.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"strings"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bryanjbelanger/desktop-hypervisor-mcp/core/artifact"
	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider"
	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider/virtualbox"
	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider/vmware"
)

var detectors = []provider.Detector{virtualbox.Detector{}, vmware.Detector{}}

// discover probes every adapter family once.
func discover(ctx context.Context) []provider.Descriptor {
	var all []provider.Descriptor
	for _, d := range detectors {
		all = append(all, d.Detect(ctx)...)
	}
	return all
}

func ready(ds []provider.Descriptor) []provider.Descriptor {
	var r []provider.Descriptor
	for _, d := range ds {
		if d.Status == provider.StatusReady {
			r = append(r, d)
		}
	}
	return r
}

// ops constructs the adapter behind a ready descriptor.
func ops(d provider.Descriptor) provider.Ops {
	if d.Kind.Family() == "vmware" {
		return vmware.New(d)
	}
	return virtualbox.New(d)
}

// selectOps resolves an explicit provider id, or auto-selects when exactly
// one provider is ready. Ambiguity is surfaced, never guessed. Detection runs
// per call: hypervisors can be installed mid-session (self-install).
func selectOps(ctx context.Context, id string) (provider.Ops, error) {
	ds := discover(ctx)
	if id != "" {
		for i := range ds {
			if ds[i].ID == id {
				if ds[i].Status != provider.StatusReady {
					return nil, fmt.Errorf("provider %s is %s: %s", id, ds[i].Status, ds[i].Remediation)
				}
				return ops(ds[i]), nil
			}
		}
		return nil, fmt.Errorf("no such provider %q", id)
	}
	r := ready(ds)
	switch len(r) {
	case 0:
		return nil, fmt.Errorf("no hypervisor is ready on this host (provider action=list shows remediation)")
	case 1:
		return ops(r[0]), nil
	default:
		var names []string
		for _, d := range r {
			names = append(names, d.ID)
		}
		return nil, fmt.Errorf("several providers are ready (%v) — pass provider to choose", names)
	}
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func out(s string, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	return text(s), nil, nil
}

func vmsOut(vms []provider.VMRef, err error) (*mcp.CallToolResult, any, error) {
	if err != nil {
		return nil, nil, err
	}
	if len(vms) == 0 {
		return text("(none)"), nil, nil
	}
	b, _ := json.MarshalIndent(vms, "", "  ")
	return text(string(b)), nil, nil
}

// ------------------------------------------------------------------ provider

type providerIn struct {
	Action string `json:"action" jsonschema:"list"`
}

func providerTool(ctx context.Context, _ *mcp.CallToolRequest, in providerIn) (*mcp.CallToolResult, any, error) {
	switch in.Action {
	case "", "list":
		b, err := json.MarshalIndent(map[string]any{"providers": discover(ctx)}, "", "  ")
		if err != nil {
			return nil, nil, err
		}
		return text(string(b)), nil, nil
	}
	return nil, nil, fmt.Errorf("unknown action %q", in.Action)
}

// ------------------------------------------------------------------ artifact

type artifactIn struct {
	Action   string `json:"action" jsonschema:"catalog|resolve|fetch"`
	Provider string `json:"provider,omitempty" jsonschema:"provider id; defaults to the only ready one"`
	Image    string `json:"image,omitempty" jsonschema:"image name (talos, talos-iso, ubuntu, kali, vagrant:org/box, …)"`
	Version  string `json:"version,omitempty" jsonschema:"version/tag where the source supports it (default latest)"`
	DryRun   bool   `json:"dry_run,omitempty" jsonschema:"fetch: report version/URL/size/checksum without downloading"`
	Dir      string `json:"dir,omitempty" jsonschema:"fetch: image cache directory (default ~/.hypervisor-images, or HV_IMAGE_DIR)"`
}

func artifactTool(ctx context.Context, _ *mcp.CallToolRequest, in artifactIn) (*mcp.CallToolResult, any, error) {
	switch in.Action {
	case "", "catalog":
		var b strings.Builder
		b.WriteString("name | kind | description\n")
		for _, s := range artifact.Catalog() {
			note := ""
			if s.Notes != "" {
				note = " — " + s.Notes
			}
			fmt.Fprintf(&b, "%s | %s | %s%s\n", s.Name, s.Kind, s.Desc, note)
		}
		b.WriteString("vagrant:org/box | vagrant | any registry box published for the selected provider\n")
		b.WriteString("\nfetch returns a path: ova/ovf/vmx → vm_lifecycle import; iso → vm_config attach_iso")
		return text(b.String()), nil, nil

	case "resolve", "fetch":
		if in.Image == "" {
			return nil, nil, fmt.Errorf("%s requires an image name (see action=catalog)", in.Action)
		}
		o, err := selectOps(ctx, in.Provider)
		if err != nil {
			return nil, nil, err
		}
		d := o.Descriptor()
		r, err := artifact.Resolve(in.Image, d)
		if err != nil {
			return nil, nil, fmt.Errorf("cannot resolve %q for %s: %w", in.Image, d.ID, err)
		}
		if in.Action == "resolve" {
			b, _ := json.MarshalIndent(r, "", "  ")
			return text(string(b)), nil, nil
		}
		res, err := artifact.Fetch(ctx, r, in.Version, in.Dir, in.DryRun)
		if err != nil {
			return nil, nil, err
		}
		s := res.Summary
		if r.Notes != "" {
			s += "\nnote: " + r.Notes
		}
		if !in.DryRun {
			verb := "vm_lifecycle import path="
			if res.Format == provider.FormatISO {
				verb = "vm_config attach_iso iso="
			}
			s += fmt.Sprintf("\nnext: %s%s", verb, res.Path)
		}
		return text(s), nil, nil
	}
	return nil, nil, fmt.Errorf("unknown action %q", in.Action)
}

// --------------------------------------------------------------- vm_lifecycle

type lifecycleIn struct {
	Action   string `json:"action" jsonschema:"create|start|stop|suspend|reset|delete|clone|import|export"`
	Provider string `json:"provider,omitempty" jsonschema:"provider id; defaults to the only ready one"`
	VM       string `json:"vm,omitempty" jsonschema:"VM name"`
	CPUs     int    `json:"cpus,omitempty" jsonschema:"create: vCPUs (default 2)"`
	MemoryMB int    `json:"memory_mb,omitempty" jsonschema:"create: RAM MB (default 2048)"`
	DiskGB   int    `json:"disk_gb,omitempty" jsonschema:"create: disk GB (0 = none on VirtualBox, 25 on VMware)"`
	GuestOS  string `json:"guest_os,omitempty" jsonschema:"create: provider-native guest OS id"`
	Firmware string `json:"firmware,omitempty" jsonschema:"create: efi|bios (provider default when empty)"`
	Nested   bool   `json:"nested_virt,omitempty" jsonschema:"create: enable nested virtualization"`
	GUI      bool   `json:"gui,omitempty" jsonschema:"start: show the console window (default headless)"`
	Hard     bool   `json:"hard,omitempty" jsonschema:"stop/reset: force it, skipping the guest-OS path"`
	Dest     string `json:"dest,omitempty" jsonschema:"clone: new VM name; export: output .ova path"`
	Snapshot string `json:"snapshot,omitempty" jsonschema:"clone: source snapshot"`
	Linked   bool   `json:"linked,omitempty" jsonschema:"clone: linked clone"`
	Path     string `json:"path,omitempty" jsonschema:"import: .ova/.ovf/.vmx artifact path"`
}

func lifecycleTool(ctx context.Context, _ *mcp.CallToolRequest, in lifecycleIn) (*mcp.CallToolResult, any, error) {
	o, err := selectOps(ctx, in.Provider)
	if err != nil {
		return nil, nil, err
	}
	switch in.Action {
	case "create":
		return out(o.Create(ctx, provider.CreateSpec{Name: in.VM, CPUs: in.CPUs,
			MemoryMB: in.MemoryMB, DiskGB: in.DiskGB, GuestOS: in.GuestOS,
			Firmware: in.Firmware, NestedVirt: in.Nested}))
	case "start":
		return out(o.Start(ctx, in.VM, in.GUI))
	case "stop":
		return out(o.Stop(ctx, in.VM, in.Hard))
	case "suspend":
		return out(o.Suspend(ctx, in.VM))
	case "reset":
		return out(o.Reset(ctx, in.VM, in.Hard))
	case "delete":
		return out(o.Delete(ctx, in.VM))
	case "clone":
		return out(o.Clone(ctx, provider.CloneSpec{Source: in.VM, Dest: in.Dest,
			Snapshot: in.Snapshot, Linked: in.Linked}))
	case "import":
		return out(o.ImportImage(ctx, in.Path, in.VM))
	case "export":
		return out(o.ExportOVA(ctx, in.VM, in.Dest))
	}
	return nil, nil, fmt.Errorf("unknown action %q", in.Action)
}

// ------------------------------------------------------------------- vm_info

type infoIn struct {
	Action   string `json:"action" jsonschema:"list|running|show|ip"`
	Provider string `json:"provider,omitempty" jsonschema:"provider id; defaults to the only ready one"`
	VM       string `json:"vm,omitempty" jsonschema:"VM name (show/ip)"`
	Network  string `json:"network,omitempty" jsonschema:"ip: network to search leases on (default: all)"`
}

func infoTool(ctx context.Context, _ *mcp.CallToolRequest, in infoIn) (*mcp.CallToolResult, any, error) {
	o, err := selectOps(ctx, in.Provider)
	if err != nil {
		return nil, nil, err
	}
	switch in.Action {
	case "", "list":
		return vmsOut(o.List(ctx))
	case "running":
		return vmsOut(o.Running(ctx))
	case "show":
		return out(o.Show(ctx, in.VM))
	case "ip":
		return out(o.IP(ctx, in.VM, in.Network))
	}
	return nil, nil, fmt.Errorf("unknown action %q", in.Action)
}

// ------------------------------------------------------------------ vm_config

type configIn struct {
	Action   string            `json:"action" jsonschema:"resources|nested_virt|attach_iso|guestinfo"`
	Provider string            `json:"provider,omitempty" jsonschema:"provider id; defaults to the only ready one"`
	VM       string            `json:"vm" jsonschema:"VM name"`
	CPUs     int               `json:"cpus,omitempty" jsonschema:"resources: vCPUs"`
	MemoryMB int               `json:"memory_mb,omitempty" jsonschema:"resources: RAM MB"`
	Enabled  *bool             `json:"enabled,omitempty" jsonschema:"nested_virt: on/off (default on)"`
	ISO      string            `json:"iso,omitempty" jsonschema:"attach_iso: host path; empty detaches"`
	Slot     string            `json:"slot,omitempty" jsonschema:"attach_iso: provider-native slot (SATA:1 / sata0:1)"`
	Keys     map[string]string `json:"keys,omitempty" jsonschema:"guestinfo: pre-boot key/value config (e.g. guestinfo.talos.config)"`
}

func configTool(ctx context.Context, _ *mcp.CallToolRequest, in configIn) (*mcp.CallToolResult, any, error) {
	o, err := selectOps(ctx, in.Provider)
	if err != nil {
		return nil, nil, err
	}
	switch in.Action {
	case "resources":
		return out(o.SetResources(ctx, in.VM, in.CPUs, in.MemoryMB))
	case "nested_virt":
		on := in.Enabled == nil || *in.Enabled
		return out(o.SetNestedVirt(ctx, in.VM, on))
	case "attach_iso":
		return out(o.AttachISO(ctx, in.VM, in.ISO, in.Slot))
	case "guestinfo":
		return out(o.SetGuestinfo(ctx, in.VM, in.Keys))
	}
	return nil, nil, fmt.Errorf("unknown action %q", in.Action)
}

// ------------------------------------------------------------------ snapshot

type snapshotIn struct {
	Action   string `json:"action" jsonschema:"take|restore|delete|list"`
	Provider string `json:"provider,omitempty" jsonschema:"provider id; defaults to the only ready one"`
	VM       string `json:"vm" jsonschema:"VM name"`
	Name     string `json:"name,omitempty" jsonschema:"snapshot name (restore: empty = most recent on VirtualBox)"`
	Tree     bool   `json:"tree,omitempty" jsonschema:"list: show the hierarchy"`
	Children bool   `json:"children,omitempty" jsonschema:"delete: cascade to child snapshots (VMware only)"`
}

func snapshotTool(ctx context.Context, _ *mcp.CallToolRequest, in snapshotIn) (*mcp.CallToolResult, any, error) {
	o, err := selectOps(ctx, in.Provider)
	if err != nil {
		return nil, nil, err
	}
	switch in.Action {
	case "take":
		return out(o.SnapshotTake(ctx, in.VM, in.Name))
	case "restore":
		return out(o.SnapshotRestore(ctx, in.VM, in.Name))
	case "delete":
		return out(o.SnapshotDelete(ctx, in.VM, in.Name, in.Children))
	case "list":
		return out(o.SnapshotList(ctx, in.VM, in.Tree))
	}
	return nil, nil, fmt.Errorf("unknown action %q", in.Action)
}

// --------------------------------------------------------------------- guest

type guestIn struct {
	Action   string   `json:"action" jsonschema:"exec|script|copy_in|copy_out|screenshot"`
	Provider string   `json:"provider,omitempty" jsonschema:"provider id; defaults to the only ready one"`
	VM       string   `json:"vm" jsonschema:"VM name"`
	Program  string   `json:"program,omitempty" jsonschema:"exec: absolute program path in the guest"`
	Script   string   `json:"script,omitempty" jsonschema:"script: script text for the interpreter"`
	Interp   string   `json:"interpreter,omitempty" jsonschema:"script: guest interpreter (default /bin/bash; Windows: powershell.exe path)"`
	Args     []string `json:"args,omitempty" jsonschema:"exec: program arguments"`
	Src      string   `json:"src,omitempty" jsonschema:"copy_in: host path; copy_out: guest path"`
	Dest     string   `json:"dest,omitempty" jsonschema:"copy_in: guest path; copy_out/screenshot: host path"`
}

func guestTool(ctx context.Context, _ *mcp.CallToolRequest, in guestIn) (*mcp.CallToolResult, any, error) {
	o, err := selectOps(ctx, in.Provider)
	if err != nil {
		return nil, nil, err
	}
	switch in.Action {
	case "exec":
		return out(o.GuestExec(ctx, in.VM, in.Program, in.Args))
	case "script":
		return out(o.GuestScript(ctx, in.VM, in.Interp, in.Script))
	case "copy_in":
		return out(o.GuestCopyIn(ctx, in.VM, in.Src, in.Dest))
	case "copy_out":
		return out(o.GuestCopyOut(ctx, in.VM, in.Src, in.Dest))
	case "screenshot":
		return out(o.CaptureScreen(ctx, in.VM, in.Dest))
	}
	return nil, nil, fmt.Errorf("unknown action %q", in.Action)
}

// ------------------------------------------------------------------- network

type networkIn struct {
	Action    string `json:"action" jsonschema:"ensure_cluster_network|expose_guest_port|make_iso|repack_iso"`
	Provider  string `json:"provider,omitempty" jsonschema:"provider id; defaults to the only ready one"`
	Name      string `json:"name,omitempty" jsonschema:"cluster network name (default cluster-net; VMware always uses vmnet8)"`
	CIDR      string `json:"cidr,omitempty" jsonschema:"ensure_cluster_network: subnet (default 192.168.100.0/24)"`
	Proto     string `json:"proto,omitempty" jsonschema:"expose_guest_port: tcp|udp (default tcp)"`
	GuestIP   string `json:"guest_ip,omitempty" jsonschema:"expose_guest_port: guest address"`
	GuestPort int    `json:"guest_port,omitempty" jsonschema:"expose_guest_port"`
	HostPort  int    `json:"host_port,omitempty" jsonschema:"expose_guest_port"`
	SrcDir    string `json:"src_dir,omitempty" jsonschema:"make_iso: host directory to pack"`
	Dest      string `json:"dest,omitempty" jsonschema:"make_iso: output .iso path"`
	Label     string `json:"label,omitempty" jsonschema:"make_iso: volume label (OEMDRV for kickstart)"`
	ISO       string `json:"iso,omitempty" jsonschema:"repack_iso: source installer .iso"`
	BootArgs  string `json:"boot_args,omitempty" jsonschema:"repack_iso: kernel args added to every GRUB linux line (e.g. 'autoinstall ds=nocloud')"`
	GrubCfg   string `json:"grub_cfg,omitempty" jsonschema:"repack_iso: GRUB config path inside the ISO (default /boot/grub/grub.cfg)"`
}

func networkTool(ctx context.Context, _ *mcp.CallToolRequest, in networkIn) (*mcp.CallToolResult, any, error) {
	// Host-side ISO surgery is identical on every provider — no selection.
	if in.Action == "repack_iso" {
		return out(provider.RepackISO(ctx, in.ISO, in.Dest, in.BootArgs, in.GrubCfg))
	}
	o, err := selectOps(ctx, in.Provider)
	if err != nil {
		return nil, nil, err
	}
	switch in.Action {
	case "ensure_cluster_network":
		return out(o.EnsureClusterNetwork(ctx, in.Name, in.CIDR))
	case "expose_guest_port":
		return out(o.ExposeGuestPort(ctx, in.Name, in.Proto, in.GuestIP, in.GuestPort, in.HostPort))
	case "make_iso":
		return out(o.MakeISO(ctx, in.SrcDir, in.Dest, in.Label))
	}
	return nil, nil, fmt.Errorf("unknown action %q", in.Action)
}

// ----------------------------------------------------------- execute_command

type execIn struct {
	Provider string   `json:"provider,omitempty" jsonschema:"provider id; defaults to the only ready one"`
	Args     []string `json:"args" jsonschema:"full argv after the provider CLI name (VBoxManage …/vmrun …)"`
}

func execTool(ctx context.Context, _ *mcp.CallToolRequest, in execIn) (*mcp.CallToolResult, any, error) {
	o, err := selectOps(ctx, in.Provider)
	if err != nil {
		return nil, nil, err
	}
	return out(o.Raw(ctx, in.Args))
}

// ---------------------------------------------------------------------- main

// instructions are generated from what is actually present, so a host with a
// single hypervisor never pays context for the other.
func instructions(ds []provider.Descriptor) string {
	s := "Desktop hypervisor operations across VirtualBox and VMware (Fusion/Workstation).\n" +
		"provider action=list reports installed hypervisors, capabilities, and remediation. " +
		"Neutral tools express intent; provider-native mechanism goes through execute_command. " +
		"Guest credentials come from the server environment (HV_GUEST_USER / HV_GUEST_PASSWORD), never parameters.\n"
	r := ready(ds)
	switch len(r) {
	case 0:
		s += "No hypervisor is currently ready on this host; provider list reports how to fix that.\n"
	case 1:
		s += fmt.Sprintf("One provider is ready (%s); it is selected automatically.\n", r[0].ID)
	default:
		s += "Several providers are ready — ask the user which to use, then pass its id as provider.\n"
	}
	s += "Recipe — VM from an image: artifact fetch (catalog lists images; dry_run previews) → vm_lifecycle import (or create+attach_iso for installers) → vm_config as needed → vm_lifecycle start → vm_info ip.\n" +
		"Talos on VMware: vm_config guestinfo (guestinfo.talos.config = base64 machine config) BEFORE first start — no maintenance-mode round trip. On VirtualBox: boot, vm_info ip, then apply config over the network."
	return s
}

func main() {
	ctx := context.Background()
	ds := discover(ctx)

	server := mcp.NewServer(
		&mcp.Implementation{Name: "desktop-hypervisor-mcp", Version: "0.3.0"},
		&mcp.ServerOptions{Instructions: instructions(ds)},
	)
	mcp.AddTool(server, &mcp.Tool{Name: "provider",
		Description: "Hypervisor discovery: installed providers with status, capabilities, formats, and remediation."}, providerTool)
	mcp.AddTool(server, &mcp.Tool{Name: "artifact",
		Description: "OS images by short name. catalog: what exists. resolve: which artifact fits the selected provider and host arch. fetch: download it verified (per-source checksums; Vagrant boxes are extracted to their importable file) into the shared image cache."}, artifactTool)
	mcp.AddTool(server, &mcp.Tool{Name: "vm_lifecycle",
		Description: "Create, start, stop, suspend (start resumes), reset, delete, clone (optionally linked/from snapshot), import (ova/ovf/vmx), export (ova)."}, lifecycleTool)
	mcp.AddTool(server, &mcp.Tool{Name: "vm_info",
		Description: "Read-only: list all VMs, running VMs, show one VM, or resolve a guest IP (in-guest tools, else hypervisor DHCP leases by MAC — works for agentless guests like Talos)."}, infoTool)
	mcp.AddTool(server, &mcp.Tool{Name: "vm_config",
		Description: "Configure a VM: resources (cpus/memory), nested_virt, attach_iso (empty path detaches), guestinfo (pre-boot key/value config; VMware only — inverts Talos bring-up order)."}, configTool)
	mcp.AddTool(server, &mcp.Tool{Name: "snapshot",
		Description: "Snapshots: take, restore, delete (children=true cascades on VMware), list (tree=true for hierarchy)."}, snapshotTool)
	mcp.AddTool(server, &mcp.Tool{Name: "guest",
		Description: "Inside a running guest (agent required; credentials from server env, never parameters): exec, script (interpreter + text — the one-liner path), copy_in, copy_out, screenshot."}, guestTool)
	mcp.AddTool(server, &mcp.Tool{Name: "network",
		Description: "Intent-based networking: ensure_cluster_network (shared NAT with DHCP), expose_guest_port (host→guest forward), make_iso (kickstart/seed ISOs), repack_iso (add kernel args to an installer ISO\u2019s GRUB — Ubuntu autoinstall). Native network mechanism stays behind execute_command."}, networkTool)
	mcp.AddTool(server, &mcp.Tool{Name: "execute_command",
		Description: "Raw provider escape hatch: full argv for VBoxManage or vmrun on the selected provider."}, execTool)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
	os.Exit(0)
}
