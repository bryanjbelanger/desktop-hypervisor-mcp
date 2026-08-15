---
name: hv-vm
description: Provision a VM on a desktop hypervisor (VirtualBox or VMware Fusion/Workstation) the right way — from a catalog OS image, an appliance/machine file, or an ISO install. Use when the user asks to create, set up, or spin up a VM (any OS) locally.
---

# Provision a VM

## Preflight

Requires the `desktop-hypervisor` MCP server: confirm `mcp__hypervisor__provider`
is available (loaded or deferred). Missing → stop; it's on the MCP Registry as
`io.github.bryanjbelanger/desktop-hypervisor-mcp` (binaries/.mcpb on the repo's
latest release); register with
`claude mcp add --scope user hypervisor -- <binary>` and restart the session.
`provider action=list` shows each installed hypervisor's readiness and
remediation. One ready provider is auto-selected; several → ask the user, then
pass `provider=` on every call.

Check existing state first (`vm_info action=list`) and never reuse a taken name.

## Choose the path

1. **Catalog image (preferred)** — `artifact action=catalog`, then
   `artifact action=fetch image=<entry>` (add `dry_run=true` to preview
   version/size/checksum). The artifact resolves per provider — VirtualBox and
   VMware each get an image they can import. Verified wherever the publisher
   ships a digest; warn the user when output says UNVERIFIED.
2. **User-supplied machine file** (`.ova`/`.ovf`/`.vmx`) → import path.
3. **ISO install** (no appliance exists) → create + attach path.

## Import path

- `vm_lifecycle action=import path=<file> vm=NAME`
- Size it: `vm_config action=resources vm=NAME cpus=C memory_mb=M`
  (defaults 2/2048 for a headless server; ask only if the workload is ambiguous)
- Running hypervisors inside the VM? `vm_config action=nested_virt vm=NAME`
- `vm_lifecycle action=start vm=NAME` (headless default; `gui=true` for a console)

## ISO path

- `vm_lifecycle action=create vm=NAME cpus=C memory_mb=M disk_gb=G guest_os=<id>`
  (`firmware=efi` where the OS needs it)
- `vm_config action=attach_iso vm=NAME iso=<path>`
- Start; the user drives the OS installer unless the ISO is auto-installing
  (kickstart/autoinstall — see hv-network's make_iso/repack_iso for seeding).

## Verify & report

`vm_info action=running` for state, `action=show vm=NAME` for detail,
`action=ip vm=NAME` for the address (works for agentless guests via DHCP
leases). Report name, specs, provider, how to reach it — and stock credentials
if a Vagrant-sourced image was used (usually vagrant/vagrant; advise changing).
