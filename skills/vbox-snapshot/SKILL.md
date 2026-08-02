---
name: vbox-snapshot
description: VirtualBox snapshot workflows — pre-change safety snapshots, restore/rollback, and snapshot hygiene. Use when the user asks to snapshot, roll back, restore, or protect a VM before a risky change.
---

# Snapshot workflows

## Preflight

Requires the `virtualbox` MCP server: confirm `mcp__virtualbox__snapshot` is
available (loaded or deferred). Missing → stop; register with
`claude mcp add --scope user virtualbox -- <binary>` (build from
github.com/bryanjbelanger/virtualbox-mcp-server) and restart the session.

## Pre-change safety snapshot (default habit)

Before any risky in-VM operation the user describes (upgrades, config surgery):
`snapshot action=take vm_name=NAME snapshot_name=pre-<change>-<YYYYMMDD> description='<what and why>'`
Live VMs snapshot fine (state is captured); note that a running snapshot includes
memory and is larger.

## Rollback

1. `snapshot action=list vm_name=NAME` — confirm the target exists; show the user.
2. A restore DISCARDS current disk state. If the VM is running:
   `vm_lifecycle action=poweroff` first (restore requires powered-off).
3. `snapshot action=restore snapshot_name=…` (or `action=restore_current` for the
   most recent). Start the VM again afterward.
4. Never restore without naming what will be lost (time window since snapshot).

## Hygiene

- Snapshots are deltas; long chains slow I/O and bloat disk. When a change is
  confirmed good, delete its safety snapshot: `snapshot action=delete`.
- Enumerate all VMs' chains for an audit: `vm_info action=list topic=vms`, then
  `snapshot action=list` per VM; flag chains deeper than 3 or older than 30 days.

## Report

Snapshot name/UUID taken or restored, chain depth after the operation, and
anything flagged for cleanup.
