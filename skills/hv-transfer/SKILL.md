---
name: hv-transfer
description: Clone, export, back up, and move VMs on VirtualBox or VMware — OVA export for sharing/backup, linked clones for experiments, cross-host moves. Use when the user asks to copy/clone a VM, export or back up a VM, share a VM, or move VMs to another machine.
---

# Clone, export, and move VMs

## Preflight

Requires the `desktop-hypervisor` MCP server: confirm `mcp__hypervisor__vm_lifecycle`
is available. Missing → stop; register with
`claude mcp add --scope user hypervisor -- <binary>` (MCP Registry:
`io.github.bryanjbelanger/desktop-hypervisor-mcp`) and restart the session.

## Clone (same host)

- Stop the source first (or clone from a snapshot: `snapshot=NAME`).
- `vm_lifecycle action=clone vm=SRC dest=NEW` — add `linked=true` for a fast,
  space-efficient linked clone (it depends on the source; say so), and
  `snapshot=SNAP` to clone a point in time (linked clones require one on VMware).
- Clones get fresh MAC addresses; confirm with `vm_info action=show` before
  running clone and source side by side.

## Export → OVA (backup / share)

- Stop the VM first.
- `vm_lifecycle action=export vm=NAME dest=/path/NAME.ova`
  On VMware this needs `ovftool` — if it's missing the error names the gap
  (`provider action=list` shows it up front).
- Verify the file exists, report its size, and give the user its sha256 so the
  receiving side can verify integrity.

## Import (restore / receive)

- `vm_lifecycle action=import path=X.ova vm=NEWNAME` (`.ova`/`.ovf`/`.vmx`)
- Re-check networking per the hv-network skill — the file's network config may
  not match this host — then `vm_config action=resources` if sizing changes.

## Cross-host move

Export → copy the .ova (with its hash) → import on the other machine. This is
also the supported path between hypervisor families (VirtualBox ↔ VMware),
since both sides speak OVA.

## Report

What was produced (name/path/size/sha256), identity changes (MACs), and any
follow-up the receiving side must do.
