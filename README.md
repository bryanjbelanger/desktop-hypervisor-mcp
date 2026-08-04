# Desktop Hypervisor MCP

One provider-neutral MCP server for the desktop hypervisors: **VirtualBox**,
**VMware Fusion**, and **VMware Workstation**. Nine tools, ~1,800 context
tokens, full coverage of what its two predecessor servers did in ~5,600.

It succeeds [virtualbox-mcp-server](../../tree/predecessor-virtualbox) (that
history is preserved on the `predecessor-virtualbox` branch) and the never-
released vmware-fusion-mcp-server, folding both into one capability-based
surface.

## Design

- **Capability-based, not lowest-common-denominator.** Providers advertise
  what they can actually do (`provider action=list`): guest exec, OVA
  import/export, linked clones, pre-boot guestinfo injection, DHCP-lease IP
  discovery, ISO mastering… Unsupported operations fail by naming the missing
  capability, not with a provider error.
- **Intent, not mechanism.** "Ensure the cluster nodes share a network the
  host can reach" is expressible on both families; "create a host-only
  interface" is not. The neutral tools express intent; provider-native
  mechanism stays reachable, unabstracted, through `execute_command`.
- **Artifacts resolve per provider.** The same image name gives each
  hypervisor the artifact it can actually import — Talos's `vmware-*.ova` vs
  `virtualbox-*.ova`, Vagrant's `vmware_desktop` vs `virtualbox` boxes,
  Microsoft's per-hypervisor dev VM — keyed on (image, provider family, host
  arch).

## Tools

| Tool | Actions |
|------|---------|
| `provider` | list — installed hypervisors with status, capabilities, formats, and remediation when not ready |
| `artifact` | catalog, resolve, fetch (dry_run) — verified downloads of official OS images, per provider |
| `vm_lifecycle` | create, start, stop, suspend, reset, delete, clone, import, export |
| `vm_info` | list, running, show, ip (in-guest tools, else DHCP leases by MAC — works for agentless guests like Talos) |
| `vm_config` | resources, nested_virt, attach_iso, guestinfo (pre-boot key/value injection, VMware) |
| `snapshot` | take, restore, delete, list (tree) |
| `guest` | exec, script, copy_in, copy_out, screenshot |
| `network` | ensure_cluster_network, expose_guest_port, make_iso, repack_iso |
| `execute_command` | raw `VBoxManage` / `vmrun` argv on the selected provider |

With one hypervisor installed it is selected automatically; with several, the
ambiguity is surfaced, never guessed.

## Image catalog

`artifact action=catalog` lists maintained images: Talos (appliance and metal
ISO), Ubuntu (cloud image and Vagrant), Debian, Fedora, Rocky, Alma,
openSUSE, FreeBSD, Kali, TurnKey, the Windows dev VM, plus `vagrant:org/box`
passthrough to the whole registry. Downloads are sha256-verified wherever the
publisher provides a digest (GitHub release assets always do); unverified
sources say so loudly. Vagrant boxes are fetched for the *right* provider and
auto-extracted to their importable machine file. The cache lives in
`~/.hypervisor-images` (`HV_IMAGE_DIR` overrides).

## Prerequisites

- VirtualBox (`VBoxManage`) and/or VMware Fusion / Workstation (`vmrun`).
  Detection is per-call, so a hypervisor installed mid-session is picked up.
- Nothing else: a single static Go binary, no Python, no Node.

Optional, detected at runtime: `ovftool` enables OVA import/export on VMware;
`xorriso` enables `repack_iso`.

## Building

```bash
go build -o desktop-hypervisor-mcp .
```

## Registration with Claude Code

```bash
claude mcp add --scope user hypervisor -- /path/to/desktop-hypervisor-mcp
```

Guest-operation credentials come from the server environment, never from
tool parameters:

```bash
claude mcp add --scope user hypervisor \
  --env HV_GUEST_USER=vagrant --env HV_GUEST_PASSWORD=vagrant \
  -- /path/to/desktop-hypervisor-mcp
```

(`VMRUN_GUEST_USER`/`VMRUN_GUEST_PASSWORD` are honored for compatibility.)

The server ships usage recipes in the MCP initialize handshake, generated
from what was actually detected on the host — a session on a
VirtualBox-only machine never pays context for VMware guidance.

## Permissions

Per-tool allowlisting in `.claude/settings.json` keeps prompts meaningful —
read-only tools are safe to always-allow:

```json
{
  "permissions": {
    "allow": [
      "mcp__hypervisor__provider",
      "mcp__hypervisor__vm_info",
      "mcp__hypervisor__artifact",
      "mcp__hypervisor__snapshot"
    ]
  }
}
```

Keep mutating tools (`vm_lifecycle`, `execute_command`) behind prompts
unless you accept unattended VM mutation.

## Related projects

- [talos-mcp-server](https://github.com/bryanjbelanger/talos-mcp-server) —
  Talos/Kubernetes orchestration; consumes this server for node provisioning
  and stays deliberately hypervisor-agnostic.
