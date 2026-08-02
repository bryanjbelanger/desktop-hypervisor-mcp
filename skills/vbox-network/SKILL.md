---
name: vbox-network
description: VirtualBox networking decisions and setup — NAT networks, host-only, DHCP, port-forwards, and discovering a VM's IP. Use when the user asks to network VMs together, reach a VM from the host, find a VM's IP address, or expose a guest port.
---

# VirtualBox networking

## Preflight

Requires the `virtualbox` MCP server: confirm `mcp__virtualbox__vm_info` is
available (loaded or deferred). Missing → stop; register with
`claude mcp add --scope user virtualbox -- <binary>` (build from
github.com/bryanjbelanger/virtualbox-mcp-server) and restart the session.

Check existing state first: `vm_info action=list topic=natnets` and
`topic=hostonlyifs`, `topic=dhcpservers`.

## Choosing a network type

- **Plain NAT** (`--nic1 nat`): solo VM, outbound internet only. No inter-VM, no
  host→guest without per-VM port-forward rules.
- **NAT network** (preferred for multi-VM): inter-VM + outbound + host access via
  port-forwards. Works everywhere — no kernel extensions.
  `network action=natnetwork args=['add','--netname',NET,'--network','10.0.5.0/24','--enable','--dhcp','on']`
- **Host-only**: direct host↔guest IP access, but on macOS requires VirtualBox's
  kernel extension (approval in System Settings → Privacy & Security + reboot).
  If `network action=hostonly_create` fails mentioning /dev/vboxnetctl, fall back
  to a NAT network and tell the user why. Typed flow: `hostonly_create` →
  `hostonly_configure interface=vboxnetN ip=…` → `dhcp_add`.
- **Bridged**: VM appears on the LAN; also kext-dependent on macOS; use only when
  the user explicitly wants LAN visibility.

## Finding a VM's IP (no Guest Additions needed)

1. MAC: `vm_info action=show vm_name=NAME` (NIC section, strip colons).
2. Lease: `execute_command 'dhcpserver findlease --network=NET --mac-address=XX…'`.
Guest Additions installed? `guest action=property_get key=/VirtualBox/GuestInfo/Net/0/V4/IP`.

## Host access to a guest port (NAT network)

`network action=natnetwork args=['modify','--netname',NET,'--port-forward-4','RULE:tcp:[]:HOSTPORT:[GUEST_IP]:GUESTPORT']`
Rule names must be unique; list current rules via `vm_info action=list topic=natnets`.
Remove: same call with `--port-forward-4 delete RULE`.

## Report

State what was created/changed, the addressing scheme, and exact host endpoints
(e.g. 127.0.0.1:51001 → 10.0.5.4:22).
