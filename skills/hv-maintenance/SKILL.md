---
name: hv-maintenance
description: Desktop-hypervisor inventory, health, and cleanup — find stale VMs, reclaim disk space, and remove old VMs safely, on VirtualBox or VMware. Use when the user asks what VMs exist, to clean up their hypervisor, free disk space, or remove old VMs.
---

# Inventory & cleanup

## Preflight

Requires the `desktop-hypervisor` MCP server: confirm `mcp__hypervisor__vm_info`
is available. Missing → stop; register with
`claude mcp add --scope user hypervisor -- <binary>` (MCP Registry:
`io.github.bryanjbelanger/desktop-hypervisor-mcp`) and restart the session.
Run the inventory per ready provider (`provider action=list`, then pass
`provider=` to scope each pass).

This skill is read-heavy and delete-careful: enumerate fully, propose, get
explicit confirmation, then delete.

## Inventory pass (always safe)

- `vm_info action=list` vs `action=running` → the stopped set
- `vm_info action=show vm=NAME` per stopped VM → specs and disk footprint
- `snapshot action=list vm=NAME tree=true` → deep or stale snapshot chains
  (deltas are often where the disk actually went)
- Image cache: `~/.hypervisor-images` (or `HV_IMAGE_DIR`) grows by design —
  idempotent downloads; report its size and note it's safe to prune old images.
- Provider-native orphans (unregistered disks, stale media entries) are outside
  the neutral surface: use `execute_command` with the provider's own argv
  (VBoxManage `list hdds` / `closemedium`; `vmrun listSnapshots` etc.) and say
  which provider you're driving.

## Cleanup rules

- NEVER delete without listing exactly what will be removed and getting a yes —
  a "stale" VM may be the user's long-term environment.
- VM removal: `vm_lifecycle action=stop vm=NAME hard=true` (if running) →
  `vm_lifecycle action=delete vm=NAME` (removes the VM and its files).
- Confirmed-good changes: delete their safety snapshots (see hv-snapshot).

## Report

Table: item, provider, size/age, reason flagged, action taken or proposed.
Totals for space reclaimed vs. reclaimable-pending-approval.
