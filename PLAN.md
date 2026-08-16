# desktop-hypervisor-mcp — build plan

One provider-neutral MCP server over VirtualBox, VMware Fusion, and VMware
Workstation. Replaces `virtualbox-mcp-server` and the unreleased
`vmware-fusion-mcp-server`. `talos-mcp-server` stays a separate product and
consumes this one.

**Published Aug 4–5 2026.** The public repo was renamed (predecessor history
on the `predecessor-virtualbox` branch), v0.3.0 is tagged and released with
binaries for all five targets, and the server is listed in the MCP registry
as `io.github.bryanjbelanger/desktop-hypervisor-mcp`.

## Done

- `provider/contract.go` — capability-based provider contract. `Descriptor`
  advertises status + remediation, host OS/arch, guest arches, accepted image
  formats, network modes, capabilities, storage. `Kind.Family()` groups
  Fusion and Workstation onto the shared VMware implementation.
- `provider/virtualbox/detect.go`, `provider/vmware/detect.go` — real
  detection. Capabilities are conditional on what is actually present:
  ovftool absent removes `ova_import`/`ova_export` and the OVA formats; no
  host ISO backend removes `make_iso`.
- `provider/hostfs_{unix,windows}.go` — free-space stat behind build tags.
  `syscall.Statfs` does not exist on Windows and would have broken the
  GoReleaser windows/amd64 target.
- `core/artifact/resolve.go` + tests — artifact resolution keyed on
  (image, provider family, host arch).
- `main.go` — `provider` tool with `list` and `resolve`, auto-select when one
  provider is ready, and instructions generated from what was detected.

Verified on the dev Mac (Intel, VirtualBox 7.2.14 + Fusion both installed):
both providers detect `ready`, ovftool is found via the Fusion fallback path,
`make_iso` is advertised on VMware only, and `talos` resolves to
`vmware-amd64.ova` for Fusion vs `virtualbox-amd64.ova` for VirtualBox.

`go vet` clean, tests pass, cross-compiles on all five release targets.

Context cost so far: **~264 tokens** (1 tool). For reference, the two
predecessor servers cost ~3,267 and ~2,332 tokens; loading both is ~5,600.

## Correction to an earlier assumption

`catalog.go` was described as hypervisor-neutral and liftable unchanged. It is
not. The coupling is real and load-bearing:

- `vagrantResolve` hardcodes the Vagrant provider name `"virtualbox"`.
  Boxes are published per provider; VMware needs `vmware_desktop`.
- The `talos` entry pointed at a VirtualBox-specific OVA. Talos publishes a
  separate `vmware-*.ova`.
- `windows-dev` pointed at `aka.ms/windev_VM_virtualbox`; Microsoft publishes
  a `_vmware` variant.
- `talos-iso` hardcoded `metal-amd64.iso` — the arch assumption.

So the artifact layer was a design task, not a mechanical move. That is what
`core/artifact/resolve.go` now handles, and it is the concrete form of the
"resolver selects per advertised capability" requirement.

## Config delivery differs by provider — affects bring-up order

Talos on VMware receives its machine config through a `.vmx` guestinfo key
set before first boot:

    guestinfo.talos.config = <base64 of controlplane.yaml>

That removes the entire maintenance-mode round trip the VirtualBox flow
needs. On VirtualBox the sequence is boot → wait for maintenance mode →
discover IP → `apply_config insecure=true` → reboot. On VMware it is set
key → boot → node is already configured, and the IP is only needed
afterward for bootstrap and health.

This is now `CapGuestinfoConfig` in the contract, because it inverts the
order of operations rather than swapping one call for another. The
`stand-up-talos` skill has to branch on it.

The predecessor Fusion server already has the mechanism — its `configure`
action sets an arbitrary `.vmx` key on a powered-off VM — so this is wiring,
not new capability.

## Done (continued) — neutral tool surface ported

Items 1–5 and 8 of the original list landed together (v0.2.0):

- `provider/ops.go` — the `Ops` interface: a dispatch surface, not an
  abstraction. Capability gating via `Descriptor` + `Unsupported(cap)` errors
  that name the missing capability; provider-native vocabulary passes through
  (guest OS ids, ISO slots); network is intent-only; mechanism stays in `Raw`.
- `provider/runcmd.go` — shared runner (600s bound, both streams, OS error
  preserved when streams are empty).
- `provider/virtualbox/ops.go`, `provider/vmware/ops.go` — full adapters
  ported from the predecessors, including the campaign-earned fixes: VMX
  case-insensitive key handling, hpet0 on created VMs (Windows BSOD),
  `--lax --allowExtraConfig` imports, vmnet lease parsing by MAC
  (`ip_from_dhcp` on both providers), linked-clone snapshot requirement.
- 8 tools in `main.go`: provider, vm_lifecycle, vm_info, vm_config, snapshot
  (verb is `restore`), guest, network (intent-only + make_iso), execute_command.
- Guest credentials: `HV_GUEST_USER`/`HV_GUEST_PASSWORD` env (VMRUN_* legacy
  honored), never tool parameters.

Verified live on the dev Mac against both installed hypervisors: inventory on
each, ambiguity error when neither is named, Fusion snapshot list on a real
VM, guestinfo capability rejection on VirtualBox, vmnet8 intent answer, raw
escape. Context cost: **~1,771 tokens for all 8 tools** (predecessors: ~5,600
for less coverage). `go vet` clean, cross-compiles on all five targets.

## Done (continued) — artifact fetch (item 10, v0.3.0)

`core/artifact/fetch.go` ports the download/verify/extract mechanics; the
`artifact` tool (catalog|resolve|fetch, dry_run, version, dir) supersedes
`provider resolve` — `provider` is now discovery-only. Fetch dispatches on
the resolved Kind: GitHub release assets (digest-verified), ubuntu-cloud
(SHA256SUMS), fixed URLs (explicitly UNVERIFIED), Vagrant boxes (downloaded,
then extracted to the importable machine file). Output ends with the next
tool call (`vm_lifecycle import` / `vm_config attach_iso`).

Provider-neutral decisions made here:

- **Image cache is `~/.hypervisor-images` (`HV_IMAGE_DIR` overrides), not a
  hypervisor's VM dir** — the same ISO seeds VMs on both families. The
  predecessor's `~/VirtualBox VMs/ISOs` cache is abandoned (adoption ~0).
- **Vagrant format is per-family, fixed in Resolve**: virtualbox boxes carry
  `box.ovf`, vmware_desktop boxes a `.vmx` bundle. Labeling every box "ovf"
  made `Accepts` reject boxes on a Fusion install without ovftool, even
  though vmrun imports the extracted `.vmx` directly. `extractBox` hunts for
  either and reports what it found; `Resolved` now carries `Arch` too.
- **Vagrant selection is (provider, architecture), not provider** — found
  live: Vagrant Cloud publishes per-arch entries and marks arm64 default
  for bento boxes, so the predecessor's name-only pick hands an Intel host
  an ARM image. (That bug ships in `virtualbox-mcp-server` today; dies with
  it.) Cache keys include provider and arch.

Verified: unit tests (extraction ovf/vmx, traversal rejection, arch-aware
pick, SHA256SUMS parsing, https-only), plus live stdio smoke on the dev Mac —
catalog, talos resolve on Fusion, talos-iso dry-run fetch (v1.13.8, GitHub
digest), bento/ubuntu-24.04 dry-run on Fusion returning the **amd64**
vmware_desktop box. bento no longer publishes sha256 (checksum_type "none"),
so boxes download with the UNVERIFIED warning. vet clean, five targets build.
Context cost: **~2,204 tokens for all 9 tools**.

## Correction — Talos hypervisor OVAs are gone from GitHub releases (Aug 2026)

The `talos` entry's assets were stale, found by a fetch dry-run against
v1.13.8. Two separate errors:

- `virtualbox-{arch}.ova` **never existed** as a release asset (checked back
  to v0.8.0, 2020) — the earlier "verified" note covered resolution only,
  never a fetch. There is no VirtualBox platform image anywhere (Image
  Factory has no virtualbox target either); upstream's documented VirtualBox
  path is the metal ISO. The variant now resolves to `metal-{arch}.iso`.
- `vmware-{arch}.ova` last shipped in v1.7.7; v1.8.0 (Sep 2024) moved all
  non-standard assets to Image Factory. The variant now resolves through the
  new `KindTalosFactory` (vanilla schematic, tag from the GitHub release
  feed). Factory publishes no checksum, so unlike before the OVA downloads
  UNVERIFIED; the vSphere/ovftool caveats in the Notes still stand, and
  `Variant.Kind` now optionally overrides `Source.Kind` because one image's
  variants can come from different distribution channels.

## Next — not started

7. **Skills → plugin.** Five `vbox-*` skills become hypervisor-neutral and
   take a `provider_id`. They are currently hand-symlinked from a source
   tree into `~/.claude/skills/`, which does not survive distribution.
9. Port `install.go` self-install (wire behind `CapSelfInstall`; the
   VirtualBox implementation exists in the predecessor); add the VMware
   equivalent (both products are now free).
11. **CIS hardened images → catalog entries** (waiting on the
    `cis-hardened-images` repo cutting its first GitHub release; it is still
    working through build issues — as of Aug 4 only rocky9 has packer vars
    and the workflows are stubs). The shape is already supported: one
    `KindGitHubAsset` Source per target (rocky9/10, alma9/10, cs9/10,
    ubuntu2204/2404) with per-family Variants, exactly like `talos`. GitHub
    auto-digests release assets, so our fetch verifies without parsing their
    `.ova.sha256` sidecars. **Naming contract to request of that repo**:
    date/version in the release *tag*, not the asset name (findAsset is
    exact-match against "latest"), and a hypervisor discriminator in the
    asset (`cis-<target>-<virtualbox|vmware>-amd64.ova`) — the images differ
    per hypervisor (guest agent), and today both packer sources emit
    `<vm_name>.ova` with no discriminator, which cannot be told apart in one
    release.

## Open questions — need verification before the contract hardens

- ~~Does Talos's VMware image ship open-vm-tools?~~ **Answered.** It does
  not, by default. Guest tools come from `talos-vmtoolsd`, an Image Factory
  *extension* that must be baked into a custom image, and the Sidero guide
  then runs it as a post-deployment daemonset. So `ip_from_tools` is
  unavailable for a stock Talos node on Fusion/Workstation, and
  `ip_from_dhcp` (vmnet lease file, keyed by MAC) is the only mechanism that
  works out of the box. The contract was already built this way.

- ~~Is the Talos vmware OVA importable into Fusion/Workstation?~~
  **Answered (Aug 2026).** It is. The v1.13.8 Image Factory OVA (vmx-15,
  pvscsi, vmxnet3) imported into Fusion with *plain* ovftool 5.0.0 — no
  `--lax` needed; strict mode only drops the `disk.EnableUUID` ExtraConfig,
  which `--allowExtraConfig` would keep and which desktop use doesn't need.
  The VM boots to maintenance mode: DHCP lease on vmnet8, apid on :50000,
  `talosctl version --insecure` answers with the server tag. One catch:
  ovftool imports the NIC as **bridged**, so `ip_from_dhcp` (vmnet8 lease
  file) sees nothing until `ethernet0.connectionType` is switched to `nat`.
  Recorded in the catalog Notes.
- **`vmware-fusion-local` version reads `1.17.0.25388279`** — that is the
  vmrun version, not the Fusion product version. Fine for logging, wrong if
  anything gates on a product version.
- **`defaultVMDir` for VMware is a documented default, not a query.** vmrun
  exposes no way to ask. If the user moved their VM store, free-space
  reporting is wrong.

## Blocked on you — cannot be done unattended

- **Apple Developer account + notarization.** Needed before mcpb bundles ship
  to macOS users, or Gatekeeper blocks the extracted binary. Requires a paid
  account and signing secrets in CI. Once bundles exist, add the `packages`
  block to `server.json` (mcpb type) and republish for one-click installs —
  the current registry entry is deliberately minimal.
- **Rotate `VMRUN_GUEST_PASSWORD`.** It is in `~/.claude.json` in plaintext
  and has been readable for the life of that config.

### Done Aug 4–5 2026 (were blocked, now cleared)

- Repo renamed; predecessor history on `predecessor-virtualbox` (+v1.0.0 tag).
  The old tag does not confuse Go tooling: its go.mod carries the old module
  path, so the proxy excludes it from version resolution for the new name.
- Actions set to require approval for all external contributors (fork PRs
  cannot run on the self-hosted runner unreviewed).
- MCP registry: logged in and published (v0.3.0, minimal entry, description
  capped at 100 chars by the registry).
- Runner: no new registration needed — but ARC registration does NOT follow
  GitHub rename redirects. Fixed `githubConfigUrl` in
  cluster-infra/argocd-apps/arc-runner-set-vbox-mcp-app.yaml (commit
  9d61d5f there); scale-set name and runs-on label deliberately kept as
  `arc-runner-set-vbox-mcp`.
- v0.3.0 released: five binaries + checksums.txt via GoReleaser on the
  cluster runner.

## Fold-in audit — vmware-fusion-mcp-server v0.3 coverage map (Aug 4 2026)

Every predecessor action is now in exactly one of three buckets. Nothing is
unaccounted for.

**Neutral surface** (was already ported, or added by this audit — marked +):
inventory→vm_info list · list→running · create · start/stop(gui,hard) ·
+suspend · +reset · clone(linked,snapshot) · delete · ip (tools→DHCP-lease
fallback) · import_ova/export_ova (+`--allowExtraConfig` restored on export) ·
attach_iso · make_iso · +repack_iso (provider-neutral, `provider.RepackISO`,
xorriso-gated at runtime) · configure/write_var-pre-boot→vm_config guestinfo ·
capture_screen (+creds now optional again, as in the predecessor) ·
snapshot list(+tree)/take/revert→restore/delete(+children, VMware-only
cascade) · guest run→exec · +script(interpreter) — VBox synthesizes via
`-c`/`-Command` · copy_to/from→copy_in/out.

**Deliberately raw** (`execute_command`; provider vocabulary diverges or the
operation is niche): pause/unpause · upgradevm · installTools/checkToolsState ·
connect/disconnectNamedDevice · read/writeVariable runtime scopes (guestVar on
a RUNNING VM, guestEnv) · sharedfolder add/remove/set/enable/disable ·
listHostNetworks · list/deletePortForwardings · network adapter
add/set/delete (attach intent may be promoted later if workflows demand it) ·
downloadPhotonVM · vmware-vdiskmanager beyond create.

**Superseded**: guest file-management verbs (mkdir/rmdir/exists/list_dir/
delete_file/rename/temp_file, processes/kill) — `script` covers all of them
in one action; predecessor's 8 extra actions were context spent on what a
shell one-liner does.

Known predecessor behavior NOT carried: ovftool flag passthrough on
import/export (Raw is vmrun-only). Open question: whether Raw grows a
`tool: vmrun|ovftool` switch or ovftool flags stay unreachable.

### Workstation parity fixes from the same audit

vmrun's command set is identical across Fusion/Workstation (only `-T` and
tool paths differ — already handled), but three host-layout assumptions were
macOS-only and are now fixed: `.vmwarevm` bundle naming in Create/Clone/
ImportImage (plain directory on Windows/Linux; resolveVMX tries both layouts);
vmnet subnet detection reads `/etc/vmware/networking` on Linux (same format);
lease paths were already per-GOOS. Untested on real Windows/Linux Workstation
hosts — same caveat as the predecessors.

## Coordination note → main.go claimant (cross-session, Aug 4 2026)

Your claimed main.go work is ALREADY DONE in the working tree — do not redo
from a stale read (we raced once already; any pre-race uncommitted main.go
edit of yours was likely overwritten). Current main.go wires: suspend|reset,
guest script, snapshot tree/children, repack_iso (short-circuited BEFORE
selectOps — it is host-neutral), fixed Snapshot* call sites, updated
descriptions. vet/tests green, five targets build, live-smoked on both
hypervisors. provider/* + PLAN.md committed as 656b441; main.go left
uncommitted for you to review against those signatures and commit as yours.
