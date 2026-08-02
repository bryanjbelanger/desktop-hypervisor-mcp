---
name: vbox-vm
description: Provision a VirtualBox VM the right way — from a catalog OS image, an appliance file, or an ISO install. Use when the user asks to create, set up, or spin up a VM (any OS) in VirtualBox.
---

# Provision a VirtualBox VM

## Preflight

Requires the `virtualbox` MCP server: confirm `mcp__virtualbox__vm_info` is
available (loaded or deferred) before starting. Missing → stop and have the user
register it (`claude mcp add --scope user virtualbox -- <binary>`; build from
github.com/bryanjbelanger/virtualbox-mcp-server) and restart the session. If the
tool exists but `vm_info action=list topic=vms` errors, VirtualBox/VBoxManage is
not installed on the host.

Check existing state first (`vm_info action=list topic=vms`) and never reuse a
taken name.

## Choose the path

1. **Catalog image (preferred)** — `image action=catalog`, then `image action=fetch
   name=<entry>`. Result `.ova`/`.ovf` → appliance path; `.iso` → ISO path.
   The catalog is checksum-verified where the publisher provides one; warn the
   user when output says UNVERIFIED.
2. **User-supplied appliance** (`.ova`/`.ovf`) → appliance path.
3. **ISO install** (no appliance exists) → ISO path.

## Appliance path

- `appliance action=import file_path=<path> args=['--vsys','0','--vmname',NAME]`
- Size it: `vm_config action=modify vm_name=NAME args=['--cpus',C,'--memory',M]`
  (defaults: 2/2048 desktop-less server; ask only if workload is stated ambiguously)
- Networking per the vbox-network skill (NAT network for multi-VM, plain NAT for solo).
- `vm_lifecycle action=start` (headless default; gui if the user wants a console).

## ISO path

- `vm_lifecycle action=create vm_name=NAME os_type=<VBoxManage ostype>` (list via
  `vm_info action=list topic=ostypes` when unsure), sizing params inline.
- `storage action=add_controller controller=SATA`
- `storage action=create_medium file_path='~/VirtualBox VMs/NAME/NAME.vdi' size_mb=10240+`
- `storage action=attach medium=<vdi> port=0` and
  `storage action=attach medium=<iso> medium_type=dvddrive port=1`
- Boot order DVD→disk: `vm_config action=modify args=['--boot1','dvd','--boot2','disk']`
- Start; note the user must drive the OS installer unless the image is
  auto-installing (Talos, cloud images with seed).

## Verify & report

`vm_info action=list topic=runningvms` for state; `vm_info action=show vm_name=NAME`
for specs. Report name, specs, network, how to reach it, and stock credentials if a
Vagrant-sourced image was used (advise changing them).
