---
name: hv-snapshot
description: VM snapshot workflows on VirtualBox or VMware — pre-change safety snapshots, restore/rollback, and snapshot hygiene. Use when the user asks to snapshot, roll back, restore, or protect a VM before a risky change.
---

# Snapshot workflows

## Preflight

Requires the `desktop-hypervisor` MCP server: confirm `mcp__hypervisor__snapshot`
is available. Missing → stop; register with
`claude mcp add --scope user hypervisor -- <binary>` (MCP Registry:
`io.github.bryanjbelanger/desktop-hypervisor-mcp`) and restart the session.

## Pre-change safety snapshot (default habit)

Before any risky in-VM operation the user describes (upgrades, config surgery):
`snapshot action=take vm=NAME name=pre-<change>-<YYYYMMDD>`
Running VMs snapshot fine (state is captured); note a running snapshot includes
memory and is larger.

## Rollback

1. `snapshot action=list vm=NAME tree=true` — confirm the target exists; show
   the user the hierarchy.
2. A restore DISCARDS current disk state. Stop the VM first
   (`vm_lifecycle action=stop vm=NAME`, `hard=true` if the guest hangs).
3. `snapshot action=restore vm=NAME name=SNAP` (on VirtualBox an empty name
   restores the most recent). Start the VM again afterward.
4. Never restore without naming what will be lost (time window since snapshot).

## Hygiene

- Snapshots are deltas; long chains slow I/O and bloat disk. When a change is
  confirmed good, delete its safety snapshot: `snapshot action=delete vm=NAME
  name=SNAP` (`children=true` cascades — VMware only; VirtualBox deletes merge
  into the chain one at a time).
- Audit: `vm_info action=list`, then `snapshot action=list tree=true` per VM;
  flag chains deeper than 3 or older than 30 days.

## Report

Snapshot name taken or restored, chain depth after the operation, and anything
flagged for cleanup.
