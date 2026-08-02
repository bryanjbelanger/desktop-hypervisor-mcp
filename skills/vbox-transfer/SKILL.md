---
name: vbox-transfer
description: Clone, export, back up, and move VirtualBox VMs — OVA export for sharing/backup, clones for experiments, cross-host moves. Use when the user asks to copy/clone a VM, export or back up a VM, share a VM with someone, or move VMs to another machine.
---

# Clone, export, and move VMs

## Preflight

Requires the `virtualbox` MCP server: confirm `mcp__virtualbox__vm_lifecycle` and
`mcp__virtualbox__appliance` are available (loaded or deferred). Missing → stop;
register with `claude mcp add --scope user virtualbox -- <binary>` (build from
github.com/bryanjbelanger/virtualbox-mcp-server) and restart the session.

## Clone (same host)

- VM must be powered off (or clone from a snapshot).
- `vm_lifecycle action=clone vm_name=SRC args=['--name',DEST,'--mode','machine']`
  (add `['--snapshot',NAME]` to clone from a point in time; `--options=link` for a
  fast space-efficient linked clone — note it depends on the source).
- Regenerate identity when the clone will run beside the source: VirtualBox
  re-MACs NICs by default on clone; confirm with `vm_info action=show`.

## Export → OVA (backup / share)

- Power off first (exports of running VMs fail).
- `appliance action=export vm_name=NAME file_path=/path/NAME.ova`
- Verify the file exists and report its size; suggest a sha256 for integrity when
  sharing (`download_file`'s verification works on re-import at the other end if
  the user publishes the hash).

## Import (restore / receive)

- `appliance action=import file_path=X.ova args=['--vsys','0','--vmname',NEWNAME]`
- Re-attach networks per the vbox-network skill (imports keep the OVA's network
  type, which may not match this host).

## Move (same host, different disk/folder)

`vm_lifecycle action=move vm_name=NAME args=['--folder','/new/path']` — VirtualBox
relocates disks with the VM; do not move files manually.

## Report

What was produced (name/path/size), identity changes (MACs), and any follow-up
the receiving side must do.
