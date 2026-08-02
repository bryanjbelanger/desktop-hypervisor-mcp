# VirtualBox MCP Server

An MCP server exposing the **entire VBoxManage 7.2 command surface** (51 commands) through **10 domain-organized tools** — full coverage without tool overload.

## Design

One MCP tool per VBoxManage command would mean ~51 tools: thousands of wasted context tokens per session and error-prone tool selection. Instead, each tool covers one domain and multiplexes its subcommands via an `action` parameter:

- **Typed parameters** cover the common paths (create a VM, attach a disk, take a snapshot) — no raw flags needed.
- **`args` passthrough** accepts any extra VBoxManage flags verbatim for the long tail.
- **Per-tool permissions** stay meaningful in Claude Code: always-allow read-only `vm_info`, keep prompts on `vm_lifecycle`.

## Tools & coverage map

| Tool | Actions | VBoxManage commands |
|------|---------|---------------------|
| `vm_lifecycle` | create, register, unregister, clone, move, start, stop, poweroff, pause, resume, reset, savestate, control, discard_state, adopt_state, encrypt, unattended_install | createvm, registervm, unregistervm, clonevm, movevm, startvm, controlvm, discardstate, adoptstate, encryptvm, unattended |
| `vm_config` | modify, get_extradata, set_extradata, nvram, bandwidth | modifyvm, getextradata, setextradata, modifynvram, bandwidthctl |
| `vm_info` | list, show, metrics | list, showvminfo, metrics |
| `storage` | add_controller, remove_controller, attach, detach, create_medium, modify_medium, clone_medium, close_medium, medium_info, medium_property, medium_io, encrypt_medium, check_medium_password, convert_from_raw | storagectl, storageattach, createmedium, modifymedium, clonemedium, closemedium, showmediuminfo, mediumproperty, mediumio, encryptmedium, checkmediumpwd, convertfromraw |
| `network` | hostonly_create/remove/configure, hostonlynet, natnetwork, dhcp_add/modify/remove/restart | hostonlyif, hostonlynet, natnetwork, dhcpserver |
| `snapshot` | take, delete, restore, restore_current, list, edit | snapshot |
| `guest` | run, copy_to, copy_from, mkdir, rmdir, rm, mv, stat, control, property_get/set/delete/enumerate/wait, sharedfolder_add/remove | guestcontrol, guestproperty, sharedfolder |
| `appliance` | import, export, sign, cloud, cloud_profile | import, export, signova, cloud, cloudprofile |
| `system` | set_property, extpack, update_check, usb_filter, usb_dev_source, debug_vm, obj_tracker | setproperty, extpack, updatecheck, usbfilter, usbdevsource, debugvm, objtracker |
| `execute_command` | (raw command string) | anything, incl. `internalcommands` (⚠ dangerous — can corrupt VM configs) |
| `download_file` | (url, dest_path, sha256) | — (HTTPS file fetch for ISOs/OVAs: streams to disk, verifies sha256, idempotent) |
| `image` | catalog, fetch (name, version, dry_run) | — (official OS image catalog: Talos, Ubuntu, Debian, Fedora, Rocky, Alma, CentOS Stream, openSUSE, FreeBSD, Kali, TurnKey, Windows dev, plus `vagrant:org/box` passthrough; checksum-verified where published; Vagrant boxes auto-extract to importable .ovf) |

OS-specific orchestration deliberately lives elsewhere: Talos/Kubernetes operations are the companion [talos-mcp-server](https://github.com/bryanjbelanger/talos-mcp-server)'s job — this server stays hypervisor-generic.

Every command listed by `VBoxManage commands` (7.2.14) is reachable; `internalcommands` is deliberately raw-only.

## Prerequisites

- VirtualBox 7.x with `VBoxManage` in PATH

That's it — the server is a single self-contained Go binary with **no runtime dependencies** (no Python, no Node).

## Building

Requires Go 1.22+ (build-time only; users of the binary need nothing):

```bash
go build -o virtualbox-mcp-server .
```

## Registration with Claude Code

```bash
claude mcp add --scope user virtualbox -- /Users/bryanbelanger/Projects/virtualbox-mcp-server/virtualbox-mcp-server
```

Verify with `claude mcp list` (should show `✔ Connected`), then restart Claude Code. Tools appear as `mcp__virtualbox__vm_lifecycle`, `mcp__virtualbox__vm_info`, etc. **Config changes and server edits take effect at the next session start.**

## Zero-question operation

The server ships its own usage recipes to the client in the MCP initialize handshake (`instructions`), so a fresh session can provision VMs from official OS images — network, image fetch, appliance import, boot — without asking the user for URLs, checksums, platform details, or paths. Image sources are pinned in the catalog and every download is verified wherever the publisher provides a checksum.

Claude Code's own per-tool permission prompts are separate — they belong to the user's security settings, not this server. Users who want fully unattended runs can allowlist, e.g. in `.claude/settings.json`:

```json
{
  "permissions": {
    "allow": [
      "mcp__virtualbox__vm_info",
      "mcp__virtualbox__image",
      "mcp__virtualbox__snapshot"
    ]
  }
}
```

Keeping mutating tools (`vm_lifecycle`, `storage`, `execute_command`) behind prompts is recommended; allowlist them too only if you accept unattended VM mutation.

## The image catalog

`image action=catalog` lists every source; `image action=fetch name=<entry>` downloads it. Sources by trust level:

- **Digest/checksum verified**: `talos` (this project's appliance releases, GitHub digests), `talos-iso` (Sidero), `ubuntu-cloud` (Canonical SHA256SUMS), and any Vagrant box version whose registry entry publishes a sha256.
- **Verified when the registry provides it, warned otherwise**: Vagrant-sourced entries (`ubuntu`, `debian`, `fedora`, `rocky`, `alma`, `centos-stream`, `opensuse`, `freebsd`, `kali`) — bento/Chef and the official Kali box, resolved via the Vagrant registry API; `.box` files are extracted in-process (Go stdlib, path-traversal guarded) to an `.ovf` the `appliance` tool imports directly. Boxes boot with stock `vagrant`/`vagrant` credentials — change them.
- **Unverifiable by publisher choice**: `windows-dev` (~22GB, Microsoft publishes no checksum), `turnkey-core` — both clearly flagged in output.
- **Anything else in the registry**: `vagrant:org/box` passthrough.

Classic CentOS Linux is EOL; `rocky` and `alma` are its RHEL-compatible successors and `centos-stream` is the rolling RHEL preview.

Adding another OS appliance of your own is a one-entry extension to the catalog in [catalog.go](catalog.go) — publish a release with the `.ova` as an asset and GitHub's automatic digests provide verification (`VBOX_MCP_TALOS_OVA_REPO` overrides the Talos source repo).

## Example prompts

```
"Create a VM called talos-cp-1 with 2 CPUs and 2GB RAM"          → vm_lifecycle
"Attach ~/isos/talos.iso as a DVD on the SATA controller"        → storage
"Create a host-only network with DHCP from .101 to .254"         → network
"Snapshot all my VMs before I break something"                   → snapshot + vm_info
"What OS types does VirtualBox support?"                         → vm_info (list ostypes)
```

## Extending

Typed hot paths live in each tool's input struct; everything else flows through `args`. To promote a flag to a typed parameter, add a field to the tool's `…In` struct (the `jsonschema` tag is its description) and wire it into the relevant action branch in [main.go](main.go), then rebuild.

## Distributing

`go build` cross-compiles per platform (`GOOS=linux GOARCH=amd64 go build …`), and a `.goreleaser.yml` like the one in the terraform-provider-virtualbox repo automates multi-platform release binaries.

## Bundled skills

[`skills/`](skills/) contains workflow skills for Claude Code — procedures that compose this server's tools: [vbox-vm](skills/vbox-vm/SKILL.md) (provisioning), [vbox-network](skills/vbox-network/SKILL.md) (networking & host access), [vbox-snapshot](skills/vbox-snapshot/SKILL.md) (safety workflows), [vbox-maintenance](skills/vbox-maintenance/SKILL.md) (inventory & cleanup), [vbox-transfer](skills/vbox-transfer/SKILL.md) (clone/export/move). Install by copying (or symlinking) into `~/.claude/skills/`. For Talos Kubernetes clusters, see the companion [talos-mcp-server](https://github.com/bryanjbelanger/talos-mcp-server).

## License

MIT
