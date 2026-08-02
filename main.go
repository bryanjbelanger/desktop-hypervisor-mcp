// VirtualBox MCP Server — full VBoxManage coverage, organized by domain.
//
// All 51 VBoxManage 7.2 commands are reachable through 10 tools. Each tool
// multiplexes one domain's subcommands via an `action` parameter: typed
// parameters cover the common paths, and `args` passes any extra VBoxManage
// flags through verbatim. execute_command remains the raw escape hatch (and
// the only route to the dangerous `internalcommands`).
//
// Ships as a single self-contained binary: no runtime dependencies beyond
// VBoxManage itself.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/shlex"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const commandTimeout = 600 * time.Second

// runCmd runs a host command and returns its output.
func runCmd(bin string, workingDir string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), commandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = workingDir
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	name := filepath.Base(bin)
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s %s timed out after %s", name, args[0], commandTimeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			// Nothing on either stream (e.g. exec failure) — the OS error is
			// all there is; losing it makes failures undebuggable.
			msg = err.Error()
		}
		return "", fmt.Errorf("%s %s failed: %s", name, args[0], msg)
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		// Progress-style commands (clonemedium, export…) report on stderr.
		out = strings.TrimSpace(stderr.String())
	}
	if out == "" {
		out = "(ok)"
	}
	return out, nil
}

// vboxManage locates the VBoxManage executable once. PATH covers macOS and
// Linux; the Windows installer does not touch PATH but sets
// VBOX_MSI_INSTALL_PATH (older versions: VBOX_INSTALL_PATH), with the
// Program Files default as a last resort.
var vboxManageCache struct {
	sync.Mutex
	path string
}

// resetVBoxManageCache forces re-discovery (after install_virtualbox).
func resetVBoxManageCache() {
	vboxManageCache.Lock()
	vboxManageCache.path = ""
	vboxManageCache.Unlock()
}

func vboxManage() string {
	vboxManageCache.Lock()
	defer vboxManageCache.Unlock()
	if vboxManageCache.path != "" {
		return vboxManageCache.path
	}
	vboxManageCache.path = findVBoxManage()
	return vboxManageCache.path
}

func findVBoxManage() string {
	if p, err := exec.LookPath("VBoxManage"); err == nil {
		return p
	}
	if runtime.GOOS == "windows" {
		for _, env := range []string{"VBOX_MSI_INSTALL_PATH", "VBOX_INSTALL_PATH"} {
			if dir := os.Getenv(env); dir != "" {
				if p := filepath.Join(dir, "VBoxManage.exe"); fileExists(p) {
					return p
				}
			}
		}
		if pf := os.Getenv("ProgramFiles"); pf != "" {
			if p := filepath.Join(pf, "Oracle", "VirtualBox", "VBoxManage.exe"); fileExists(p) {
				return p
			}
		}
	}
	// Fall through to the bare name so the eventual error names the binary.
	return "VBoxManage"
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// vbox runs a VBoxManage command and returns its output.
func vbox(args ...string) (string, error) {
	return runCmd(vboxManage(), "", args...)
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func need(value, name, action string) (string, error) {
	if value == "" {
		return "", fmt.Errorf("'%s' is required for action '%s'", name, action)
	}
	return value, nil
}

func orDefault(v string, def string) string {
	if v == "" {
		return def
	}
	return v
}

func orDefaultInt(v, def int) int {
	if v == 0 {
		return def
	}
	return v
}

// boolDefault reads an optional bool with a default for when it's omitted.
func boolDefault(v *bool, def bool) bool {
	if v == nil {
		return def
	}
	return *v
}

// ---------------------------------------------------------------- vm_lifecycle

type vmLifecycleIn struct {
	Action      string   `json:"action" jsonschema:"create|register|unregister|clone|move|start|stop|poweroff|pause|resume|reset|savestate|control|discard_state|adopt_state|encrypt|unattended_install"`
	VMName      string   `json:"vm_name,omitempty" jsonschema:"name or UUID of the VM"`
	OSType      string   `json:"os_type,omitempty" jsonschema:"guest OS type for create (default Other; e.g. Linux26_64, Ubuntu_64)"`
	MemoryMB    int      `json:"memory_mb,omitempty" jsonschema:"RAM in MB for create (default 1024)"`
	CPUs        int      `json:"cpus,omitempty" jsonschema:"CPU count for create (default 1)"`
	VRAMMB      int      `json:"vram_mb,omitempty" jsonschema:"video RAM in MB for create (default 8)"`
	Headless    *bool    `json:"headless,omitempty" jsonschema:"start without GUI (default true)"`
	DeleteFiles *bool    `json:"delete_files,omitempty" jsonschema:"unregister also deletes files (default true)"`
	ControlCmd  string   `json:"control_cmd,omitempty" jsonschema:"controlvm subcommand string for action=control (e.g. 'setlinkstate1 on')"`
	Args        []string `json:"args,omitempty" jsonschema:"extra VBoxManage flags passed through verbatim"`
}

func vmLifecycle(_ context.Context, _ *mcp.CallToolRequest, in vmLifecycleIn) (*mcp.CallToolResult, any, error) {
	switch in.Action {
	case "create":
		name, err := need(in.VMName, "vm_name", in.Action)
		if err != nil {
			return nil, nil, err
		}
		mem := orDefaultInt(in.MemoryMB, 1024)
		cpus := orDefaultInt(in.CPUs, 1)
		vram := orDefaultInt(in.VRAMMB, 8)
		out, err := vbox(append([]string{"createvm", "--name", name, "--ostype",
			orDefault(in.OSType, "Other"), "--register"}, in.Args...)...)
		if err != nil {
			return nil, nil, err
		}
		if _, err := vbox("modifyvm", name, "--memory", strconv.Itoa(mem),
			"--cpus", strconv.Itoa(cpus), "--vram", strconv.Itoa(vram)); err != nil {
			return nil, nil, err
		}
		return text(fmt.Sprintf("%s\nConfigured: %dMB RAM, %d CPU(s), %dMB VRAM", out, mem, cpus, vram)), nil, nil
	case "register":
		return run1(in.Action, in.VMName, "vm_name", func(v string) []string {
			return append([]string{"registervm", v}, in.Args...)
		})
	case "unregister":
		name, err := need(in.VMName, "vm_name", in.Action)
		if err != nil {
			return nil, nil, err
		}
		argv := []string{"unregistervm", name}
		if boolDefault(in.DeleteFiles, true) {
			argv = append(argv, "--delete")
		}
		return runArgv(append(argv, in.Args...))
	case "clone":
		return run1(in.Action, in.VMName, "vm_name", func(v string) []string {
			return append([]string{"clonevm", v, "--register"}, in.Args...)
		})
	case "move":
		return run1(in.Action, in.VMName, "vm_name", func(v string) []string {
			return append([]string{"movevm", v}, in.Args...)
		})
	case "start":
		mode := "headless"
		if !boolDefault(in.Headless, true) {
			mode = "gui"
		}
		return run1(in.Action, in.VMName, "vm_name", func(v string) []string {
			return append([]string{"startvm", v, "--type", mode}, in.Args...)
		})
	case "stop", "poweroff", "pause", "resume", "reset", "savestate":
		sub := in.Action
		if sub == "stop" {
			sub = "acpipowerbutton"
		}
		return run1(in.Action, in.VMName, "vm_name", func(v string) []string {
			return append([]string{"controlvm", v, sub}, in.Args...)
		})
	case "control":
		name, err := need(in.VMName, "vm_name", in.Action)
		if err != nil {
			return nil, nil, err
		}
		cmd, err := need(in.ControlCmd, "control_cmd", in.Action)
		if err != nil {
			return nil, nil, err
		}
		parts, err := shlex.Split(cmd)
		if err != nil {
			return nil, nil, fmt.Errorf("bad control_cmd: %w", err)
		}
		return runArgv(append(append([]string{"controlvm", name}, parts...), in.Args...))
	case "discard_state":
		return run1(in.Action, in.VMName, "vm_name", func(v string) []string {
			return append([]string{"discardstate", v}, in.Args...)
		})
	case "adopt_state":
		return run1(in.Action, in.VMName, "vm_name", func(v string) []string {
			return append([]string{"adoptstate", v}, in.Args...)
		})
	case "encrypt":
		return run1(in.Action, in.VMName, "vm_name", func(v string) []string {
			return append([]string{"encryptvm", v}, in.Args...)
		})
	case "unattended_install":
		return run1(in.Action, in.VMName, "vm_name", func(v string) []string {
			return append([]string{"unattended", "install", v}, in.Args...)
		})
	}
	return nil, nil, fmt.Errorf("unknown action: %s", in.Action)
}

// run1 validates one required param then runs the built argv.
func run1(action, value, name string, build func(string) []string) (*mcp.CallToolResult, any, error) {
	v, err := need(value, name, action)
	if err != nil {
		return nil, nil, err
	}
	return runArgv(build(v))
}

func runArgv(argv []string) (*mcp.CallToolResult, any, error) {
	out, err := vbox(argv...)
	if err != nil {
		return nil, nil, err
	}
	return text(out), nil, nil
}

// ------------------------------------------------------------------ vm_config

type vmConfigIn struct {
	Action string   `json:"action" jsonschema:"modify|get_extradata|set_extradata|nvram|bandwidth"`
	VMName string   `json:"vm_name" jsonschema:"name or UUID of the VM"`
	Key    string   `json:"key,omitempty" jsonschema:"extradata key (get_extradata defaults to enumerate)"`
	Value  *string  `json:"value,omitempty" jsonschema:"extradata value; omit to delete the key"`
	Args   []string `json:"args,omitempty" jsonschema:"flags: modifyvm flags for modify, subcommands for nvram/bandwidth"`
}

func vmConfig(_ context.Context, _ *mcp.CallToolRequest, in vmConfigIn) (*mcp.CallToolResult, any, error) {
	if _, err := need(in.VMName, "vm_name", in.Action); err != nil {
		return nil, nil, err
	}
	switch in.Action {
	case "modify":
		if len(in.Args) == 0 {
			return nil, nil, fmt.Errorf("action 'modify' needs modifyvm flags in args")
		}
		return runArgv(append([]string{"modifyvm", in.VMName}, in.Args...))
	case "get_extradata":
		return runArgv([]string{"getextradata", in.VMName, orDefault(in.Key, "enumerate")})
	case "set_extradata":
		key, err := need(in.Key, "key", in.Action)
		if err != nil {
			return nil, nil, err
		}
		argv := []string{"setextradata", in.VMName, key}
		if in.Value != nil {
			argv = append(argv, *in.Value)
		}
		return runArgv(argv)
	case "nvram":
		return runArgv(append([]string{"modifynvram", in.VMName}, in.Args...))
	case "bandwidth":
		return runArgv(append([]string{"bandwidthctl", in.VMName}, in.Args...))
	}
	return nil, nil, fmt.Errorf("unknown action: %s", in.Action)
}

// -------------------------------------------------------------------- vm_info

type vmInfoIn struct {
	Action string   `json:"action" jsonschema:"list|show|metrics"`
	Topic  string   `json:"topic,omitempty" jsonschema:"list topic (default vms): vms, runningvms, ostypes, hostinfo, hostonlyifs, natnets, dhcpservers, hdds, dvds, systemproperties, extpacks, usbhost, …"`
	VMName string   `json:"vm_name,omitempty" jsonschema:"VM for action=show"`
	Args   []string `json:"args,omitempty" jsonschema:"extra flags; for metrics the subcommand, e.g. ['query','*']"`
}

func vmInfo(_ context.Context, _ *mcp.CallToolRequest, in vmInfoIn) (*mcp.CallToolResult, any, error) {
	switch in.Action {
	case "list":
		return runArgv(append([]string{"list", orDefault(in.Topic, "vms")}, in.Args...))
	case "show":
		return run1(in.Action, in.VMName, "vm_name", func(v string) []string {
			return append([]string{"showvminfo", v}, in.Args...)
		})
	case "metrics":
		if len(in.Args) == 0 {
			return nil, nil, fmt.Errorf("action 'metrics' needs a metrics subcommand in args, e.g. ['query','*']")
		}
		return runArgv(append([]string{"metrics"}, in.Args...))
	}
	return nil, nil, fmt.Errorf("unknown action: %s", in.Action)
}

// -------------------------------------------------------------------- storage

type storageIn struct {
	Action         string   `json:"action" jsonschema:"add_controller|remove_controller|attach|detach|create_medium|modify_medium|clone_medium|close_medium|medium_info|medium_property|medium_io|encrypt_medium|check_medium_password|convert_from_raw"`
	VMName         string   `json:"vm_name,omitempty" jsonschema:"VM for controller/attach actions"`
	Controller     string   `json:"controller,omitempty" jsonschema:"controller name (default SATA)"`
	ControllerType string   `json:"controller_type,omitempty" jsonschema:"controller bus for add_controller: sata|ide|scsi|sas|usb|pcie (default sata)"`
	Port           int      `json:"port,omitempty" jsonschema:"controller port (default 0)"`
	Device         int      `json:"device,omitempty" jsonschema:"device slot (default 0)"`
	MediumType     string   `json:"medium_type,omitempty" jsonschema:"hdd|dvddrive|fdd (default hdd)"`
	Medium         string   `json:"medium,omitempty" jsonschema:"medium path/UUID; 'emptydrive' for an empty DVD"`
	FilePath       string   `json:"file_path,omitempty" jsonschema:"file path for create_medium / clone_medium target"`
	SizeMB         int      `json:"size_mb,omitempty" jsonschema:"size in MB for create_medium"`
	DiskFormat     string   `json:"disk_format,omitempty" jsonschema:"VDI|VMDK|VHD (default VDI)"`
	Args           []string `json:"args,omitempty" jsonschema:"extra VBoxManage flags passed through verbatim"`
}

func storageTool(_ context.Context, _ *mcp.CallToolRequest, in storageIn) (*mcp.CallToolResult, any, error) {
	ctl := orDefault(in.Controller, "SATA")
	switch in.Action {
	case "add_controller":
		return run1(in.Action, in.VMName, "vm_name", func(v string) []string {
			return append([]string{"storagectl", v, "--name", ctl, "--add",
				orDefault(in.ControllerType, "sata")}, in.Args...)
		})
	case "remove_controller":
		return run1(in.Action, in.VMName, "vm_name", func(v string) []string {
			return []string{"storagectl", v, "--name", ctl, "--remove"}
		})
	case "attach", "detach":
		name, err := need(in.VMName, "vm_name", in.Action)
		if err != nil {
			return nil, nil, err
		}
		med := "none"
		if in.Action == "attach" {
			if med, err = need(in.Medium, "medium", in.Action); err != nil {
				return nil, nil, err
			}
		}
		return runArgv(append([]string{"storageattach", name,
			"--storagectl", ctl, "--port", strconv.Itoa(in.Port), "--device", strconv.Itoa(in.Device),
			"--type", orDefault(in.MediumType, "hdd"), "--medium", med}, in.Args...))
	case "create_medium":
		path, err := need(in.FilePath, "file_path", in.Action)
		if err != nil {
			return nil, nil, err
		}
		if in.SizeMB == 0 {
			return nil, nil, fmt.Errorf("'size_mb' is required for action 'create_medium'")
		}
		return runArgv(append([]string{"createmedium", "disk", "--filename", path,
			"--size", strconv.Itoa(in.SizeMB), "--format", orDefault(in.DiskFormat, "VDI")}, in.Args...))
	case "modify_medium":
		return run1(in.Action, in.Medium, "medium", func(m string) []string {
			return append([]string{"modifymedium", m}, in.Args...)
		})
	case "clone_medium":
		med, err := need(in.Medium, "medium", in.Action)
		if err != nil {
			return nil, nil, err
		}
		target, err := need(in.FilePath, "file_path", in.Action)
		if err != nil {
			return nil, nil, err
		}
		return runArgv(append([]string{"clonemedium", med, target}, in.Args...))
	case "close_medium":
		return run1(in.Action, in.Medium, "medium", func(m string) []string {
			return append([]string{"closemedium", m}, in.Args...)
		})
	case "medium_info":
		return run1(in.Action, in.Medium, "medium", func(m string) []string {
			return []string{"showmediuminfo", m}
		})
	case "medium_property":
		return runArgv(append([]string{"mediumproperty"}, in.Args...))
	case "medium_io":
		return runArgv(append([]string{"mediumio"}, in.Args...))
	case "encrypt_medium":
		return run1(in.Action, in.Medium, "medium", func(m string) []string {
			return append([]string{"encryptmedium", m}, in.Args...)
		})
	case "check_medium_password":
		return run1(in.Action, in.Medium, "medium", func(m string) []string {
			return append([]string{"checkmediumpwd", m}, in.Args...)
		})
	case "convert_from_raw":
		return runArgv(append([]string{"convertfromraw"}, in.Args...))
	}
	return nil, nil, fmt.Errorf("unknown action: %s", in.Action)
}

// -------------------------------------------------------------------- network

type networkIn struct {
	Action    string   `json:"action" jsonschema:"hostonly_create|hostonly_remove|hostonly_configure|hostonlynet|natnetwork|dhcp_add|dhcp_modify|dhcp_remove|dhcp_restart"`
	Interface string   `json:"interface,omitempty" jsonschema:"host-only interface name, e.g. vboxnet0"`
	IP        string   `json:"ip,omitempty" jsonschema:"interface or DHCP server IP"`
	Netmask   string   `json:"netmask,omitempty" jsonschema:"netmask (default 255.255.255.0)"`
	LowerIP   string   `json:"lower_ip,omitempty" jsonschema:"DHCP range lower bound"`
	UpperIP   string   `json:"upper_ip,omitempty" jsonschema:"DHCP range upper bound"`
	Args      []string `json:"args,omitempty" jsonschema:"extra flags; full subcommand for hostonlynet/natnetwork"`
}

func networkTool(_ context.Context, _ *mcp.CallToolRequest, in networkIn) (*mcp.CallToolResult, any, error) {
	mask := orDefault(in.Netmask, "255.255.255.0")
	switch in.Action {
	case "hostonly_create":
		return runArgv([]string{"hostonlyif", "create"})
	case "hostonly_remove":
		return run1(in.Action, in.Interface, "interface", func(i string) []string {
			return []string{"hostonlyif", "remove", i}
		})
	case "hostonly_configure":
		iface, err := need(in.Interface, "interface", in.Action)
		if err != nil {
			return nil, nil, err
		}
		ip, err := need(in.IP, "ip", in.Action)
		if err != nil {
			return nil, nil, err
		}
		return runArgv(append([]string{"hostonlyif", "ipconfig", iface, "--ip", ip, "--netmask", mask}, in.Args...))
	case "hostonlynet":
		return runArgv(append([]string{"hostonlynet"}, in.Args...))
	case "natnetwork":
		return runArgv(append([]string{"natnetwork"}, in.Args...))
	case "dhcp_add":
		iface, err := need(in.Interface, "interface", in.Action)
		if err != nil {
			return nil, nil, err
		}
		ip, err := need(in.IP, "ip", in.Action)
		if err != nil {
			return nil, nil, err
		}
		lower, err := need(in.LowerIP, "lower_ip", in.Action)
		if err != nil {
			return nil, nil, err
		}
		upper, err := need(in.UpperIP, "upper_ip", in.Action)
		if err != nil {
			return nil, nil, err
		}
		return runArgv(append([]string{"dhcpserver", "add", "--ifname", iface, "--ip", ip,
			"--netmask", mask, "--lowerip", lower, "--upperip", upper, "--enable"}, in.Args...))
	case "dhcp_modify":
		return run1(in.Action, in.Interface, "interface", func(i string) []string {
			return append([]string{"dhcpserver", "modify", "--ifname", i}, in.Args...)
		})
	case "dhcp_remove":
		return run1(in.Action, in.Interface, "interface", func(i string) []string {
			return []string{"dhcpserver", "remove", "--ifname", i}
		})
	case "dhcp_restart":
		return run1(in.Action, in.Interface, "interface", func(i string) []string {
			return []string{"dhcpserver", "restart", "--ifname", i}
		})
	}
	return nil, nil, fmt.Errorf("unknown action: %s", in.Action)
}

// ------------------------------------------------------------------- snapshot

type snapshotIn struct {
	Action       string   `json:"action" jsonschema:"take|delete|restore|restore_current|list|edit"`
	VMName       string   `json:"vm_name" jsonschema:"name or UUID of the VM"`
	SnapshotName string   `json:"snapshot_name,omitempty" jsonschema:"snapshot name (required except restore_current/list)"`
	Description  string   `json:"description,omitempty" jsonschema:"description for take"`
	Args         []string `json:"args,omitempty" jsonschema:"extra flags passed through verbatim"`
}

func snapshotTool(_ context.Context, _ *mcp.CallToolRequest, in snapshotIn) (*mcp.CallToolResult, any, error) {
	if _, err := need(in.VMName, "vm_name", in.Action); err != nil {
		return nil, nil, err
	}
	switch in.Action {
	case "take":
		name, err := need(in.SnapshotName, "snapshot_name", in.Action)
		if err != nil {
			return nil, nil, err
		}
		argv := []string{"snapshot", in.VMName, "take", name}
		if in.Description != "" {
			argv = append(argv, "--description", in.Description)
		}
		return runArgv(append(argv, in.Args...))
	case "delete", "restore", "edit":
		name, err := need(in.SnapshotName, "snapshot_name", in.Action)
		if err != nil {
			return nil, nil, err
		}
		return runArgv(append([]string{"snapshot", in.VMName, in.Action, name}, in.Args...))
	case "restore_current":
		return runArgv([]string{"snapshot", in.VMName, "restorecurrent"})
	case "list":
		return runArgv(append([]string{"snapshot", in.VMName, "list"}, in.Args...))
	}
	return nil, nil, fmt.Errorf("unknown action: %s", in.Action)
}

// ---------------------------------------------------------------------- guest

type guestIn struct {
	Action     string   `json:"action" jsonschema:"run|copy_to|copy_from|mkdir|rmdir|rm|mv|stat|control|property_get|property_set|property_delete|property_enumerate|property_wait|sharedfolder_add|sharedfolder_remove"`
	VMName     string   `json:"vm_name" jsonschema:"name or UUID of the VM"`
	Key        string   `json:"key,omitempty" jsonschema:"guest property name"`
	Value      *string  `json:"value,omitempty" jsonschema:"guest property value; omit to clear"`
	FolderName string   `json:"folder_name,omitempty" jsonschema:"shared folder name"`
	HostPath   string   `json:"host_path,omitempty" jsonschema:"host path for sharedfolder_add"`
	Args       []string `json:"args,omitempty" jsonschema:"guestcontrol flags; needs --username/--password for guest commands"`
}

func guestTool(_ context.Context, _ *mcp.CallToolRequest, in guestIn) (*mcp.CallToolResult, any, error) {
	if _, err := need(in.VMName, "vm_name", in.Action); err != nil {
		return nil, nil, err
	}
	gc := map[string]string{"run": "run", "copy_to": "copyto", "copy_from": "copyfrom",
		"mkdir": "mkdir", "rmdir": "rmdir", "rm": "rm", "mv": "mv", "stat": "stat"}
	if sub, ok := gc[in.Action]; ok {
		return runArgv(append([]string{"guestcontrol", in.VMName, sub}, in.Args...))
	}
	switch in.Action {
	case "control":
		return runArgv(append([]string{"guestcontrol", in.VMName}, in.Args...))
	case "property_get":
		return run1(in.Action, in.Key, "key", func(k string) []string {
			return []string{"guestproperty", "get", in.VMName, k}
		})
	case "property_set":
		key, err := need(in.Key, "key", in.Action)
		if err != nil {
			return nil, nil, err
		}
		argv := []string{"guestproperty", "set", in.VMName, key}
		if in.Value != nil {
			argv = append(argv, *in.Value)
		}
		return runArgv(argv)
	case "property_delete":
		return run1(in.Action, in.Key, "key", func(k string) []string {
			return []string{"guestproperty", "delete", in.VMName, k}
		})
	case "property_enumerate":
		return runArgv(append([]string{"guestproperty", "enumerate", in.VMName}, in.Args...))
	case "property_wait":
		return run1(in.Action, in.Key, "key", func(k string) []string {
			return append([]string{"guestproperty", "wait", in.VMName, k}, in.Args...)
		})
	case "sharedfolder_add":
		fname, err := need(in.FolderName, "folder_name", in.Action)
		if err != nil {
			return nil, nil, err
		}
		hpath, err := need(in.HostPath, "host_path", in.Action)
		if err != nil {
			return nil, nil, err
		}
		return runArgv(append([]string{"sharedfolder", "add", in.VMName,
			"--name", fname, "--hostpath", hpath}, in.Args...))
	case "sharedfolder_remove":
		return run1(in.Action, in.FolderName, "folder_name", func(f string) []string {
			return append([]string{"sharedfolder", "remove", in.VMName, "--name", f}, in.Args...)
		})
	}
	return nil, nil, fmt.Errorf("unknown action: %s", in.Action)
}

// ------------------------------------------------------------------ appliance

type applianceIn struct {
	Action   string   `json:"action" jsonschema:"import|export|sign|cloud|cloud_profile"`
	FilePath string   `json:"file_path,omitempty" jsonschema:"OVA/OVF path (import/sign) or output path (export)"`
	VMName   string   `json:"vm_name,omitempty" jsonschema:"VM to export"`
	Args     []string `json:"args,omitempty" jsonschema:"extra flags; full subcommand for cloud/cloud_profile"`
}

func applianceTool(_ context.Context, _ *mcp.CallToolRequest, in applianceIn) (*mcp.CallToolResult, any, error) {
	switch in.Action {
	case "import":
		return run1(in.Action, in.FilePath, "file_path", func(f string) []string {
			return append([]string{"import", f}, in.Args...)
		})
	case "export":
		name, err := need(in.VMName, "vm_name", in.Action)
		if err != nil {
			return nil, nil, err
		}
		out, err := need(in.FilePath, "file_path", in.Action)
		if err != nil {
			return nil, nil, err
		}
		return runArgv(append([]string{"export", name, "--output", out}, in.Args...))
	case "sign":
		return run1(in.Action, in.FilePath, "file_path", func(f string) []string {
			return append([]string{"signova", f}, in.Args...)
		})
	case "cloud":
		return runArgv(append([]string{"cloud"}, in.Args...))
	case "cloud_profile":
		return runArgv(append([]string{"cloudprofile"}, in.Args...))
	}
	return nil, nil, fmt.Errorf("unknown action: %s", in.Action)
}

// --------------------------------------------------------------------- system

type systemIn struct {
	Action  string   `json:"action" jsonschema:"set_property|extpack|update_check|usb_filter|usb_dev_source|debug_vm|obj_tracker|host_check|install_virtualbox"`
	Key     string   `json:"key,omitempty" jsonschema:"property name for set_property"`
	Value   string   `json:"value,omitempty" jsonschema:"property value for set_property"`
	Version string   `json:"version,omitempty" jsonschema:"install_virtualbox: pin a VirtualBox version (default: Oracle's LATEST-STABLE)"`
	DryRun  bool     `json:"dry_run,omitempty" jsonschema:"install_virtualbox: report platform, resolved version, asset, and sha256 without downloading or installing"`
	Args    []string `json:"args,omitempty" jsonschema:"subcommand and flags for the other actions"`
}

func systemTool(ctx context.Context, _ *mcp.CallToolRequest, in systemIn) (*mcp.CallToolResult, any, error) {
	switch in.Action {
	case "host_check":
		return text(hostCheck()), nil, nil
	case "install_virtualbox":
		out, err := installVirtualBox(ctx, installVBoxIn{Version: in.Version, DryRun: in.DryRun})
		if err != nil {
			return nil, nil, err
		}
		return text(out), nil, nil
	case "set_property":
		key, err := need(in.Key, "key", in.Action)
		if err != nil {
			return nil, nil, err
		}
		val, err := need(in.Value, "value", in.Action)
		if err != nil {
			return nil, nil, err
		}
		return runArgv([]string{"setproperty", key, val})
	case "extpack":
		return runArgv(append([]string{"extpack"}, in.Args...))
	case "update_check":
		if len(in.Args) == 0 {
			return runArgv([]string{"updatecheck", "perform"})
		}
		return runArgv(append([]string{"updatecheck"}, in.Args...))
	case "usb_filter":
		return runArgv(append([]string{"usbfilter"}, in.Args...))
	case "usb_dev_source":
		return runArgv(append([]string{"usbdevsource"}, in.Args...))
	case "debug_vm":
		return runArgv(append([]string{"debugvm"}, in.Args...))
	case "obj_tracker":
		return runArgv(append([]string{"objtracker"}, in.Args...))
	}
	return nil, nil, fmt.Errorf("unknown action: %s", in.Action)
}

// ------------------------------------------------------------ execute_command

type execIn struct {
	Command string `json:"command" jsonschema:"VBoxManage command without the 'VBoxManage' prefix; quote arguments containing spaces"`
}

func execTool(_ context.Context, _ *mcp.CallToolRequest, in execIn) (*mcp.CallToolResult, any, error) {
	if in.Command == "" {
		return nil, nil, fmt.Errorf("'command' is required")
	}
	parts, err := shlex.Split(in.Command)
	if err != nil {
		return nil, nil, fmt.Errorf("bad command: %w", err)
	}
	return runArgv(parts)
}

// -------------------------------------------------------------- download_file

type downloadIn struct {
	URL      string `json:"url" jsonschema:"HTTPS URL to download (e.g. a Talos ISO or an appliance OVA)"`
	DestPath string `json:"dest_path" jsonschema:"absolute local path to save to; parent directories are created"`
	SHA256   string `json:"sha256,omitempty" jsonschema:"expected SHA-256 hex digest; the download fails on mismatch"`
}

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
	client := &http.Client{Timeout: 30 * time.Minute}
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

func downloadTool(ctx context.Context, _ *mcp.CallToolRequest, in downloadIn) (*mcp.CallToolResult, any, error) {
	if in.URL == "" || in.DestPath == "" {
		return nil, nil, fmt.Errorf("'url' and 'dest_path' are required")
	}
	out, err := fetchHTTPS(ctx, in.URL, in.DestPath, in.SHA256)
	if err != nil {
		return nil, nil, err
	}
	return text(out), nil, nil
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
	client := &http.Client{Timeout: 30 * time.Second}
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

func (r *ghRelease) findAsset(match func(string) bool) (*ghAsset, error) {
	for i := range r.Assets {
		if match(r.Assets[i].Name) {
			return &r.Assets[i], nil
		}
	}
	return nil, fmt.Errorf("no matching asset in release %s (assets: %v)", r.TagName, func() []string {
		names := make([]string, len(r.Assets))
		for i, a := range r.Assets {
			names[i] = a.Name
		}
		return names
	}())
}

func (a *ghAsset) sha256() string {
	return strings.TrimPrefix(a.Digest, "sha256:")
}

// ---------------------------------------------------------------- talos_image

// Image sources baked in so end users need no URLs: the OVA comes from this
// project's companion appliance repo, the ISO from the official Sidero release.
const defaultTalosOVARepo = "bryanjbelanger/talos-virtualbox-vm"
const talosRepo = "siderolabs/talos"

func talosOVARepoName() string {
	if r := os.Getenv("VBOX_MCP_TALOS_OVA_REPO"); r != "" {
		return r
	}
	return defaultTalosOVARepo
}

func imageDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return "."
	}
	return filepath.Join(home, "VirtualBox VMs", "ISOs")
}

// ----------------------------------------------------------------------- main

// serverInstructions is sent to clients at initialize time so any model driving
// this server knows the end-to-end recipes without asking the user for URLs,
// checksums, platform details, or file paths.
const serverInstructions = `VirtualBox management server. Prefer typed parameters; pass exotic VBoxManage flags via args. vm_info is read-only and always safe.

OS images: image action=catalog lists every available official image (Talos, Ubuntu, Debian, Fedora, Rocky, Alma, CentOS Stream, openSUSE, FreeBSD, Kali, TurnKey, Windows dev, and any vagrant:org/box). image action=fetch downloads it verified and returns a path: .ova/.ovf → appliance import; .iso → storage attach as dvddrive.

Recipe — VM from an appliance image:
1. Optional shared network: network action=natnetwork args=['add','--netname',NET,'--network','192.168.100.0/24','--enable','--dhcp','on']
2. image action=fetch name=<catalog entry>  (verified download, returns local path; idempotent)
3. appliance action=import file_path=<path> args=['--vsys','0','--vmname',NAME]
   then vm_config action=modify vm_name=NAME args=['--cpus',C,'--memory',M,'--nic1','natnetwork','--nat-network1',NET]
4. vm_lifecycle action=start (headless default); verify with vm_info action=list topic=runningvms

Recipe — fresh VM from an ISO: vm_lifecycle create → storage add_controller / create_medium / attach (ISO as dvddrive, medium_type=dvddrive, boot order dvd→disk) → vm_lifecycle start.
Node IPs on a NAT network: get the VM's MAC from vm_info action=show, then execute_command 'dhcpserver findlease --network=NET --mac-address=XX…'. Host access to guest ports: natnetwork port-forward rules via the network tool.
Vagrant-sourced images (.ovf) boot with the box's stock credentials (usually vagrant/vagrant) — advise users to change them.`

func main() {
	server := mcp.NewServer(&mcp.Implementation{Name: "virtualbox-mcp-server", Version: "2.5.0"},
		&mcp.ServerOptions{Instructions: serverInstructions})

	mcp.AddTool(server, &mcp.Tool{Name: "vm_lifecycle", Description: "VM lifecycle: create/register/unregister/clone/move/start VMs and control running ones. 'control' sends any controlvm subcommand (acpipowerbutton, poweroff, pause, resume, reset, savestate, …) via control_cmd. 'unattended_install' drives unattended guest OS install (pass flags in args)."}, vmLifecycle)
	mcp.AddTool(server, &mcp.Tool{Name: "vm_config", Description: "VM configuration: 'modify' passes modifyvm flags via args (e.g. ['--nic1','hostonly','--hostonlyadapter1','vboxnet0']). Also extradata get/set, NVRAM (modifynvram), and bandwidth groups (bandwidthctl)."}, vmConfig)
	mcp.AddTool(server, &mcp.Tool{Name: "vm_info", Description: "Read-only inspection: 'list' any VBoxManage list topic (vms, runningvms, ostypes, hostinfo, hostonlyifs, natnets, dhcpservers, hdds, dvds, systemproperties, extpacks, usbhost, …); 'show' full VM details; 'metrics' performance metrics (metrics subcommand in args)."}, vmInfo)
	mcp.AddTool(server, &mcp.Tool{Name: "storage", Description: "Storage: controllers (add/remove), medium attach/detach, and disk-image management (create/clone/modify/close/info/property/io/encrypt/convert). attach: controller+port+medium_type+medium ('emptydrive' for empty DVD). create_medium: file_path+size_mb."}, storageTool)
	mcp.AddTool(server, &mcp.Tool{Name: "network", Description: "Host networking: host-only interfaces (create/remove/configure ip), host-only networks, NAT networks, and DHCP servers (add/modify/remove/restart). Typed params cover the common host-only + DHCP flow; other flags via args."}, networkTool)
	mcp.AddTool(server, &mcp.Tool{Name: "snapshot", Description: "Snapshots: take/delete/restore/restore_current/list/edit for a VM."}, snapshotTool)
	mcp.AddTool(server, &mcp.Tool{Name: "guest", Description: "Guest interaction (needs Guest Additions for guestcontrol): run commands in the guest, copy files to/from, mkdir/rm/stat; guest properties get/set/delete/enumerate/wait; shared folders add/remove. guestcontrol needs --username/--password in args."}, guestTool)
	mcp.AddTool(server, &mcp.Tool{Name: "appliance", Description: "Appliances & cloud: import/export OVA/OVF (file_path + flags in args), sign an OVA (signova), and Oracle cloud integration (cloud/cloudprofile subcommands via args)."}, applianceTool)
	mcp.AddTool(server, &mcp.Tool{Name: "system", Description: "Host/system administration: host_check (is VirtualBox installed? platform report), install_virtualbox (self-install from Oracle's official mirror, SHA256SUMS-verified, per-platform: macOS admin dialog / Linux deb-rpm-run via passwordless sudo / Windows silent installer; dry_run previews), global properties (setproperty), extension packs, update checks, USB filters & device sources, VM debugging, object tracker."}, systemTool)
	mcp.AddTool(server, &mcp.Tool{Name: "execute_command", Description: "Raw escape hatch: run any VBoxManage command verbatim (without the 'VBoxManage' prefix). Quote arguments containing spaces. The only route to 'internalcommands' (use with care — those can corrupt VM configs)."}, execTool)
	mcp.AddTool(server, &mcp.Tool{Name: "download_file", Description: "Download a file over HTTPS to a local path (ISO images, appliance OVAs, checksums). Streams to dest_path creating parent directories; verifies sha256 when provided; no-op if the file already exists with a matching checksum."}, downloadTool)
	mcp.AddTool(server, &mcp.Tool{Name: "image", Description: "Official, maintained OS image catalog. action=catalog lists sources (talos, talos-iso, ubuntu, ubuntu-cloud, debian, fedora, rocky, alma, opensuse, freebsd, kali, turnkey-core, windows-dev, or vagrant:org/box passthrough). action=fetch resolves a version, downloads with checksum verification where the publisher provides one, extracts Vagrant boxes to an importable .ovf, and returns the local path. Idempotent; dry_run resolves without downloading."}, imageTool)

	if err := server.Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
}
