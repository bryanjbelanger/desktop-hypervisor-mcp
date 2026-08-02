---
name: vbox-maintenance
description: VirtualBox inventory, health, and cleanup — find stale VMs, orphaned disks, inaccessible media, and reclaim disk space safely. Use when the user asks what VMs exist, to clean up VirtualBox, free disk space, or remove old VMs/disks.
---

# Inventory & cleanup

## Preflight

Requires the `virtualbox` MCP server: confirm `mcp__virtualbox__vm_info` is
available (loaded or deferred). Missing → stop; register with
`claude mcp add --scope user virtualbox -- <binary>` (build from
github.com/bryanjbelanger/virtualbox-mcp-server) and restart the session.

This skill is read-heavy and delete-careful: enumerate fully, propose, get
explicit confirmation, then delete.

## Inventory pass (always safe)

- `vm_info action=list topic=vms` vs `topic=runningvms` → stopped set
- `vm_info action=show vm_name=…` per stopped VM → last state change time
- `vm_info action=list topic=hdds` → all registered disks; flag entries whose
  state is "inaccessible" or whose parent VM no longer exists (orphans)
- `vm_info action=list topic=natnets` / `topic=hostonlyifs` / `topic=dhcpservers`
  → networks no VM references
- Image cache: `~/VirtualBox VMs/ISOs/` grows by design (idempotent downloads);
  report its size via `vm_info`-adjacent shell is NOT available — instead note the
  directory for the user.

## Cleanup rules

- NEVER delete without listing exactly what will be removed and getting a yes —
  a "stale" VM may be the user's long-term environment.
- VM removal: `vm_lifecycle action=poweroff` (if running) →
  `vm_lifecycle action=unregister` (delete_files defaults true — say so).
- Orphaned disk: `storage action=close_medium medium=<path> args=['--delete']`.
- Inaccessible media entries (file gone): `storage action=close_medium` without
  --delete just deregisters.
- Unused networks: `network action=natnetwork args=['remove','--netname',…]` /
  `network action=dhcp_remove` + `action=hostonly_remove`.

## Report

Table: item, type, size/age, reason flagged, action taken or proposed. Totals for
space reclaimed vs. reclaimable-pending-approval.
