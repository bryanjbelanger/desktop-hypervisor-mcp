---
name: hv-network
description: Desktop-hypervisor networking — shared cluster networks, reaching a guest from the host, discovering a VM's IP, exposing guest ports, and building boot/seed ISOs. Use when the user asks to network VMs together, find a VM's IP, expose a guest port, or make a kickstart/seed ISO, on VirtualBox or VMware.
---

# Hypervisor networking & host access

## Preflight

Requires the `desktop-hypervisor` MCP server: confirm `mcp__hypervisor__provider`
is available. Missing → stop; register with
`claude mcp add --scope user hypervisor -- <binary>` (MCP Registry:
`io.github.bryanjbelanger/desktop-hypervisor-mcp`) and restart the session.
Multiple ready providers → ask which, pass `provider=` on every call.

## VMs that share a network the host can reach

`network action=ensure_cluster_network name=NET` (default `cluster-net`,
`cidr=` to override the 192.168.100.0/24 default). This is intent-based — the
provider picks its own mechanism (VMware always uses vmnet8), and nothing here
depends on macOS kernel-extension approval. Provider-native network surgery
beyond that intent stays available via `execute_command`.

## Finding a VM's IP

`vm_info action=ip vm=NAME` — uses in-guest tools when present, else DHCP
leases by MAC (`network=` narrows the search). Works for agentless guests
like Talos.

## Reaching a guest from the host

Provider reality first: **VMware's NAT subnet is host-reachable** — try the
guest IP directly. **VirtualBox NAT networks are not** — forward a port:
`network action=expose_guest_port guest_ip=IP guest_port=P host_port=H`
(`proto=udp` when needed). Then target `127.0.0.1:H`. Pick unique host ports
and report the mapping.

## Boot and seed ISOs

- `network action=make_iso src_dir=DIR dest=OUT.iso label=LABEL` — pack a
  directory (label `OEMDRV` makes Anaconda pick up a kickstart automatically).
- `network action=repack_iso iso=SRC.iso dest=OUT.iso boot_args='…'` — add
  kernel args to an installer's GRUB entries (e.g. `inst.ks=…`, `autoinstall`).
  Requires `xorriso` on the host; the tool names the gap if it's missing.

## Report

State what exists/changed, the addressing scheme, and exact host endpoints
(e.g. 127.0.0.1:51001 → 192.168.100.4:22).
