package main

// VirtualBox self-installation: resolve the latest stable release from
// Oracle's official mirror, verify against Oracle's published SHA256SUMS,
// and drive the platform's native install path. No silent privilege
// escalation exists on any OS — macOS raises the system's own admin dialog,
// Linux requires passwordless sudo (or root), Windows requires an elevated
// context — and the errors say exactly what to do when that's missing.

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

const vboxMirror = "https://download.virtualbox.org/virtualbox/"

func installFetchText(ctx context.Context, url string) (string, error) {
	// Retry transient failures: virtualized NAT DNS proxies (VMware vmnat is
	// a known case) intermittently drop responses to Go's parallel A/AAAA
	// lookups, so a single attempt is flaky in exactly the environments this
	// action targets.
	var lastErr error
	for attempt := 0; attempt < 4; attempt++ {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(time.Duration(attempt) * 2 * time.Second):
			}
		}
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return "", err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		if resp.StatusCode != 200 {
			resp.Body.Close()
			return "", fmt.Errorf("GET %s: %s", url, resp.Status)
		}
		b, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		return string(b), err
	}
	return "", fmt.Errorf("after 4 attempts: %w", lastErr)
}

type osRelease struct{ ID, IDLike, VersionID, Codename string }

func readOSRelease() osRelease {
	var r osRelease
	data, err := os.ReadFile("/etc/os-release")
	if err != nil {
		return r
	}
	for _, line := range strings.Split(string(data), "\n") {
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		v = strings.Trim(v, `"`)
		switch k {
		case "ID":
			r.ID = v
		case "ID_LIKE":
			r.IDLike = v
		case "VERSION_ID":
			r.VersionID = v
		case "VERSION_CODENAME":
			r.Codename = v
		}
	}
	return r
}

func (r osRelease) family() string {
	all := r.ID + " " + r.IDLike
	switch {
	case strings.Contains(all, "debian") || strings.Contains(all, "ubuntu"):
		return "deb"
	case strings.Contains(all, "fedora") && r.ID == "fedora":
		return "fedora"
	case strings.Contains(all, "rhel") || strings.Contains(all, "fedora") ||
		strings.Contains(all, "centos"):
		return "el"
	case strings.Contains(all, "suse"):
		return "suse"
	}
	return ""
}

// pickInstallerAsset chooses the right file from Oracle's release directory
// listing for this host. Linux falls back to the distro-agnostic .run
// installer when no packaged build matches.
func pickInstallerAsset(files []string, rel osRelease) (string, error) {
	find := func(substrs ...string) string {
		for _, f := range files {
			ok := true
			for _, s := range substrs {
				if !strings.Contains(f, s) {
					ok = false
					break
				}
			}
			if ok {
				return f
			}
		}
		return ""
	}
	switch runtime.GOOS {
	case "darwin":
		if f := find("OSX.dmg"); f != "" {
			return f, nil
		}
		return "", fmt.Errorf("no macOS dmg in release listing")
	case "windows":
		if f := find("Win.exe"); f != "" {
			return f, nil
		}
		return "", fmt.Errorf("no Windows installer in release listing")
	case "linux":
		switch rel.family() {
		case "deb":
			if rel.Codename != "" {
				if f := find("~"+rel.Codename, "amd64.deb"); f != "" {
					return f, nil
				}
			}
			distro := map[bool]string{true: "~Debian~", false: "~Ubuntu~"}[rel.ID == "debian"]
			var cands []string
			for _, f := range files {
				if strings.Contains(f, distro) && strings.HasSuffix(f, "amd64.deb") {
					cands = append(cands, f)
				}
			}
			if len(cands) > 0 {
				sort.Strings(cands)
				return cands[len(cands)-1], nil
			}
		case "fedora":
			want, _ := strconv.Atoi(strings.Split(rel.VersionID, ".")[0])
			best, bestN := "", -1
			re := regexp.MustCompile(`fedora(\d+)`)
			for _, f := range files {
				if m := re.FindStringSubmatch(f); m != nil && strings.HasSuffix(f, ".rpm") {
					n, _ := strconv.Atoi(m[1])
					if n > bestN && (want == 0 || n <= want) {
						best, bestN = f, n
					}
				}
			}
			if best != "" {
				return best, nil
			}
		case "el":
			major := strings.Split(rel.VersionID, ".")[0]
			if f := find("el"+major, ".rpm"); f != "" {
				return f, nil
			}
		case "suse":
			if f := find("openSUSE", ".rpm"); f != "" {
				return f, nil
			}
		}
		// Distro-agnostic fallback.
		if f := find("Linux_amd64.run"); f != "" {
			return f, nil
		}
		return "", fmt.Errorf("no Linux installer matched (distro %q) and no .run fallback found", rel.ID)
	}
	return "", fmt.Errorf("unsupported platform: %s", runtime.GOOS)
}

// sudoN runs a command via non-interactive sudo, with a clean error when
// passwordless sudo is unavailable.
func sudoN(manual string, args ...string) (string, error) {
	if os.Geteuid() == 0 {
		return runCmd(args[0], "", args[1:]...)
	}
	if _, err := runCmd("sudo", "", "-n", "true"); err != nil {
		return "", fmt.Errorf("installing requires root and passwordless sudo is not available — run this on the host once instead: sudo %s", manual)
	}
	return runCmd("sudo", "", append([]string{"-n"}, args...)...)
}

type installVBoxIn struct {
	Version string
	DryRun  bool
}

// linuxEnsureKernelModule builds/loads vboxdrv when VBoxManage reports it
// missing: install matching kernel headers, then run Oracle's vboxconfig.
func linuxEnsureKernelModule(versionOut string) string {
	if runtime.GOOS != "linux" || !strings.Contains(versionOut, "vboxdrv") {
		return ""
	}
	kernel, _ := runCmd("uname", "", "-r")
	var log []string
	if _, err := exec.LookPath("apt-get"); err == nil {
		out, err := sudoN("apt-get install -y linux-headers-"+kernel,
			"apt-get", "install", "-y", "linux-headers-"+kernel)
		if err != nil {
			out2, _ := sudoN("apt-get install -y linux-headers-amd64",
				"apt-get", "install", "-y", "linux-headers-amd64")
			out += " | fallback: " + out2
		}
		log = append(log, "headers: "+lastLine(out))
	} else if _, err := exec.LookPath("dnf"); err == nil {
		out, err := sudoN("dnf install -y kernel-devel-"+kernel+" gcc make perl elfutils-libelf-devel",
			"dnf", "install", "-y", "kernel-devel-"+kernel, "gcc", "make", "perl", "elfutils-libelf-devel")
		if err != nil {
			out2, _ := sudoN("dnf install -y kernel-devel gcc make perl elfutils-libelf-devel",
				"dnf", "install", "-y", "kernel-devel", "gcc", "make", "perl", "elfutils-libelf-devel")
			out += " | fallback: " + out2
		}
		log = append(log, "headers: "+lastLine(out))
	}
	// Headers only help if they match the RUNNING kernel; distro repos drop
	// old versions, so a stale kernel may have no installable match.
	if _, err := os.Stat("/usr/src/kernels/" + kernel); err != nil {
		if _, err := os.Stat("/lib/modules/" + kernel + "/build"); err != nil {
			log = append(log, fmt.Sprintf("NO HEADERS MATCH the running kernel %s (repos likely dropped it) — upgrade the kernel and reboot, then rerun install_virtualbox", kernel))
			return "kernel module: " + strings.Join(log, "; ")
		}
	}
	out, err := sudoN("/sbin/vboxconfig", "/sbin/vboxconfig")
	if err != nil {
		log = append(log, "vboxconfig FAILED: "+err.Error())
	} else {
		log = append(log, "vboxconfig: "+lastLine(out))
	}
	return "kernel module: " + strings.Join(log, "; ")
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}

func installVirtualBox(ctx context.Context, in installVBoxIn) (string, error) {
	if p, err := exec.LookPath("VBoxManage"); err == nil && !in.DryRun {
		v, _ := runCmd(p, "", "--version")
		// Installed but module missing (headers absent at install time) is
		// still broken — repair rather than declare success.
		if fix := linuxEnsureKernelModule(v); fix != "" {
			v2, _ := runCmd(p, "", "--version")
			return fmt.Sprintf("VirtualBox already installed: %s\n%s\nversion now: %s", p, fix, v2), nil
		}
		return fmt.Sprintf("VirtualBox already installed: %s (%s) — nothing to do", p, v), nil
	}

	version := in.Version
	if version == "" {
		v, err := installFetchText(ctx, vboxMirror+"LATEST-STABLE.TXT")
		if err != nil {
			return "", fmt.Errorf("resolving latest VirtualBox version: %w", err)
		}
		version = strings.TrimSpace(v)
	}
	base := vboxMirror + version + "/"

	index, err := installFetchText(ctx, base)
	if err != nil {
		return "", err
	}
	var files []string
	for _, m := range regexp.MustCompile(`href="([^"?/]+)"`).FindAllStringSubmatch(index, -1) {
		files = append(files, m[1])
	}

	rel := readOSRelease()
	asset, err := pickInstallerAsset(files, rel)
	if err != nil {
		return "", err
	}

	sums, err := installFetchText(ctx, base+"SHA256SUMS")
	if err != nil {
		return "", fmt.Errorf("fetching Oracle SHA256SUMS: %w", err)
	}
	var sha string
	for _, line := range strings.Split(sums, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && strings.TrimPrefix(fields[1], "*") == asset {
			sha = fields[0]
			break
		}
	}
	if sha == "" {
		return "", fmt.Errorf("%s not present in Oracle's SHA256SUMS — refusing unverified install", asset)
	}

	home, _ := os.UserHomeDir()
	dest := filepath.Join(home, "VirtualBox VMs", "installers", asset)

	plan := fmt.Sprintf("platform: %s/%s (distro %q)\nversion: %s\nasset: %s\nsha256: %s (Oracle-published)\ndest: %s",
		runtime.GOOS, runtime.GOARCH, rel.ID, version, asset, sha, dest)
	if in.DryRun {
		return "DRY RUN — would install:\n" + plan, nil
	}

	if _, err := fetchHTTPS(ctx, base+asset, dest, sha); err != nil {
		return "", err
	}

	var out string
	switch runtime.GOOS {
	case "darwin":
		if _, err := runCmd("hdiutil", "", "attach", "-nobrowse", dest); err != nil {
			return "", err
		}
		defer runCmd("hdiutil", "", "detach", "/Volumes/VirtualBox", "-force")
		pkgs, _ := filepath.Glob("/Volumes/VirtualBox*/VirtualBox.pkg")
		if len(pkgs) == 0 {
			return "", fmt.Errorf("VirtualBox.pkg not found in mounted dmg")
		}
		// The OS's own admin-credentials dialog appears; the password goes to
		// macOS, never through this process's arguments.
		out, err = runCmd("osascript", "", "-e",
			fmt.Sprintf(`do shell script "installer -pkg '%s' -target /" with administrator privileges`, pkgs[0]))
		if err != nil {
			return "", fmt.Errorf("%w (macOS may also require kernel-extension approval in System Settings → Privacy & Security afterwards)", err)
		}
	case "linux":
		switch {
		case strings.HasSuffix(dest, ".deb"):
			// Fresh images ship stale package indexes; installing the deb's
			// dependencies against them 404s on moved mirror versions.
			_, _ = sudoN("apt-get update", "apt-get", "update")
			out, err = sudoN("apt-get install -y "+dest, "apt-get", "install", "-y", dest)
		case strings.HasSuffix(dest, ".rpm"):
			out, err = sudoN("dnf install -y "+dest, "dnf", "install", "-y", dest)
		default: // .run
			out, err = sudoN("sh "+dest, "sh", dest)
		}
		if err != nil {
			return "", fmt.Errorf("%w (kernel module build may need headers: deb: linux-headers-$(uname -r); rpm: kernel-devel gcc make)", err)
		}
	case "windows":
		out, err = runCmd(dest, "", "--silent", "--ignore-reboot")
		if err != nil {
			return "", fmt.Errorf("%w (the installer needs an elevated context — run from an Administrator shell: %s --silent)", err, dest)
		}
	}

	resetVBoxManageCache()
	verify, verr := vbox("--version")
	if verr != nil {
		return "", fmt.Errorf("install ran but VBoxManage still not found: %v", verr)
	}
	extra := linuxEnsureKernelModule(verify)
	if extra != "" {
		verify2, _ := vbox("--version")
		extra += "\nversion after module fix: " + verify2
	}
	return fmt.Sprintf("installed VirtualBox %s\n%s\nVBoxManage --version: %s\n%s\n%s", version, plan, verify, out, extra), nil
}

func hostCheck() string {
	p := vboxManage()
	rel := readOSRelease()
	distro := ""
	if runtime.GOOS == "linux" {
		distro = fmt.Sprintf(" distro=%s %s", rel.ID, rel.VersionID)
	}
	if v, err := runCmd(p, "", "--version"); err == nil {
		return fmt.Sprintf("platform: %s/%s%s\nVBoxManage: %s (version %s)", runtime.GOOS, runtime.GOARCH, distro, p, v)
	}
	return fmt.Sprintf("platform: %s/%s%s\nVBoxManage: NOT FOUND — system action=install_virtualbox can install it (dry_run=true to preview)", runtime.GOOS, runtime.GOARCH, distro)
}
