package provider

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// RepackISO copies an installer ISO, adding kernel arguments to its GRUB
// entries. Provider-neutral: it is pure host-side ISO surgery, identical for
// every hypervisor, so it lives here rather than in Ops. Needed for
// Ubuntu/Subiquity: autoinstall config on a CIDATA volume is read, but the
// installer still waits for human confirmation unless `autoinstall` is on the
// kernel command line — and only the ISO's boot config can put it there.
//
// Requires xorriso on the host (the only portable tool that can patch one
// file and replay El Torito/EFI boot records); absence is a clear error, not
// a capability, because it is host tooling rather than hypervisor ability.
func RepackISO(ctx context.Context, src, dest, bootArgs, grubCfg string) (string, error) {
	if _, err := exec.LookPath("xorriso"); err != nil {
		return "", fmt.Errorf("repack_iso needs xorriso on the host (brew/apt/dnf install xorriso)")
	}
	if grubCfg == "" {
		grubCfg = "/boot/grub/grub.cfg"
	}
	tmp, err := os.MkdirTemp("", "repack-iso-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(tmp)

	// Pull out just the GRUB config rather than unpacking the whole image.
	local := filepath.Join(tmp, "grub.cfg")
	if _, err := RunCmd(ctx, "xorriso", "-osirrox", "on", "-indev", src,
		"-extract", grubCfg, local); err != nil {
		return "", fmt.Errorf("extracting %s from %s: %w", grubCfg, src, err)
	}
	// xorriso restores the ISO's permissions, which are read-only — make it
	// writable before patching, or the rewrite fails and the repack would
	// silently ship an unmodified config.
	if err := os.Chmod(local, 0o644); err != nil {
		return "", err
	}
	data, err := os.ReadFile(local)
	if err != nil {
		return "", err
	}
	// Append the args to every kernel line. GRUB's separator is `---`: args
	// after it go to the booted system, so insert before it when present.
	lines := strings.Split(string(data), "\n")
	patched := 0
	for i, ln := range lines {
		// Split on any whitespace: real ISOs tab-separate the directive from
		// its path (`linux\t/casper/vmlinuz`), so a "linux " prefix test
		// silently matches nothing.
		fields := strings.Fields(ln)
		if len(fields) == 0 || (fields[0] != "linux" && fields[0] != "linuxefi") {
			continue
		}
		if strings.Contains(ln, bootArgs) {
			continue // already present — keep repacks idempotent
		}
		if idx := strings.LastIndex(ln, " ---"); idx != -1 {
			lines[i] = ln[:idx] + " " + bootArgs + ln[idx:]
		} else {
			lines[i] = ln + " " + bootArgs
		}
		patched++
	}
	if patched == 0 {
		return "", fmt.Errorf("no GRUB kernel lines found in %s — wrong grub_cfg path, or the ISO already carries these args", grubCfg)
	}
	if err := os.WriteFile(local, []byte(strings.Join(lines, "\n")), 0o644); err != nil {
		return "", err
	}
	if err := os.Remove(dest); err != nil && !os.IsNotExist(err) {
		return "", err
	}
	// `-boot_image any replay` carries the original El Torito/EFI boot
	// records across, so the repacked ISO stays bootable on BIOS and UEFI.
	if _, err := RunCmd(ctx, "xorriso",
		"-indev", src, "-outdev", dest,
		"-boot_image", "any", "replay",
		"-overwrite", "on",
		"-map", local, grubCfg,
		"-commit"); err != nil {
		return "", fmt.Errorf("repacking to %s: %w", dest, err)
	}
	return fmt.Sprintf("repacked %s → %s\npatched %d GRUB kernel line(s) in %s with: %s",
		src, dest, patched, grubCfg, bootArgs), nil
}
