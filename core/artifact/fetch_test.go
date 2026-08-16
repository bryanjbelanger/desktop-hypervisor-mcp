package artifact

import (
	"archive/tar"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider"
)

// writeBox builds a .box archive (tar, optionally gzipped) with the given
// member names, each holding a little placeholder content.
func writeBox(t *testing.T, path string, gzipped bool, members ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var w io.WriteCloser = f
	if gzipped {
		w = gzip.NewWriter(f)
		defer w.Close()
	}
	tw := tar.NewWriter(w)
	defer tw.Close()
	for _, m := range members {
		body := []byte("content of " + m)
		if err := tw.WriteHeader(&tar.Header{
			Name: m, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body)),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
}

// A virtualbox box yields its .ovf.
func TestExtractBoxOVF(t *testing.T) {
	dir := t.TempDir()
	box := filepath.Join(dir, "vb.box")
	writeBox(t, box, true, "box.ovf", "box-disk001.vmdk", "Vagrantfile")
	machine, err := extractBox(box, filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(machine) != "box.ovf" {
		t.Errorf("machine = %q, want box.ovf", machine)
	}
}

// A vmware_desktop box has no .ovf — its .vmx is the importable file. Plain
// (non-gzipped) tars must also extract.
func TestExtractBoxVMX(t *testing.T) {
	dir := t.TempDir()
	box := filepath.Join(dir, "vmw.box")
	writeBox(t, box, false, "metadata.json", "vm.vmx", "disk-cl1.vmdk")
	machine, err := extractBox(box, filepath.Join(dir, "out"))
	if err != nil {
		t.Fatal(err)
	}
	if filepath.Base(machine) != "vm.vmx" {
		t.Errorf("machine = %q, want vm.vmx", machine)
	}
	body, err := os.ReadFile(machine)
	if err != nil {
		t.Fatal(err)
	}
	if string(body) != "content of vm.vmx" {
		t.Errorf("extracted content = %q", body)
	}
}

func TestExtractBoxRejectsUnsafePaths(t *testing.T) {
	dir := t.TempDir()
	box := filepath.Join(dir, "evil.box")
	writeBox(t, box, true, "../escape.ovf")
	if _, err := extractBox(box, filepath.Join(dir, "out")); err == nil {
		t.Fatal("path traversal must be rejected")
	}
}

func TestExtractBoxWithoutMachineFileErrors(t *testing.T) {
	dir := t.TempDir()
	box := filepath.Join(dir, "empty.box")
	writeBox(t, box, true, "metadata.json")
	if _, err := extractBox(box, filepath.Join(dir, "out")); err == nil {
		t.Fatal("box without .ovf/.vmx must error")
	}
}

func TestFindSHA256Sum(t *testing.T) {
	sums := "abc123  ubuntu-24.04-server-cloudimg-amd64.ova\n" +
		"def456 *ubuntu-24.04-server-cloudimg-arm64.ova\n"
	if got := findSHA256Sum(sums, "ubuntu-24.04-server-cloudimg-amd64.ova"); got != "abc123" {
		t.Errorf("plain entry: got %q", got)
	}
	if got := findSHA256Sum(sums, "ubuntu-24.04-server-cloudimg-arm64.ova"); got != "def456" {
		t.Errorf("binary-mode entry: got %q", got)
	}
	if got := findSHA256Sum(sums, "missing.ova"); got != "" {
		t.Errorf("missing entry: got %q", got)
	}
}

func TestFetchRejectsHTTP(t *testing.T) {
	if _, err := fetchHTTPS(t.Context(), "http://example.com/x.iso", filepath.Join(t.TempDir(), "x.iso"), ""); err == nil {
		t.Fatal("plain http must be rejected")
	}
}

func TestDefaultDirEnvOverride(t *testing.T) {
	t.Setenv("HV_IMAGE_DIR", "/somewhere/images")
	if got := DefaultDir(); got != "/somewhere/images" {
		t.Errorf("DefaultDir = %q", got)
	}
}

// The formats a fetch reports must be ones the resolving provider accepts;
// for vagrant that means the box's actual content, not a blanket "ovf".
func TestVagrantFormatPerFamily(t *testing.T) {
	vb, err := Resolve("ubuntu", desc(provider.KindVirtualBox, "amd64"))
	if err != nil {
		t.Fatal(err)
	}
	if vb.Format != provider.FormatOVF {
		t.Errorf("virtualbox box format = %s, want ovf", vb.Format)
	}
	// VMware without ovftool accepts vmx but not ovf — the box must still
	// resolve, because its content is a .vmx bundle.
	noOVF := desc(provider.KindFusion, "amd64",
		provider.FormatVMX, provider.FormatVMDK, provider.FormatISO)
	vm, err := Resolve("ubuntu", noOVF)
	if err != nil {
		t.Fatalf("vagrant box must resolve for vmware without ovftool: %v", err)
	}
	if vm.Format != provider.FormatVMX {
		t.Errorf("vmware box format = %s, want vmx", vm.Format)
	}
}

func TestFormatOf(t *testing.T) {
	if f := formatOf("/x/box.ovf", provider.FormatVMX); f != provider.FormatOVF {
		t.Errorf("ovf: %s", f)
	}
	if f := formatOf("/x/vm.VMX", provider.FormatOVF); f != provider.FormatVMX {
		t.Errorf("vmx: %s", f)
	}
	if f := formatOf("/x/weird.bin", provider.FormatOVA); f != provider.FormatOVA {
		t.Errorf("fallback: %s", f)
	}
}

// Vagrant Cloud lists one entry per (provider, architecture) and often marks
// arm64 as the default; a name-only pick would hand an Intel host an ARM
// image. Observed live against bento/ubuntu-24.04 on the Intel dev Mac.
func TestVagrantProviderPickIsArchAware(t *testing.T) {
	v := vagrantVersion{Providers: []vagrantProviderEntry{
		{Name: "vmware_desktop", Architecture: "arm64", DownloadURL: "arm"},
		{Name: "vmware_desktop", Architecture: "amd64", DownloadURL: "intel"},
	}}
	if p := v.provider("vmware_desktop", "amd64"); p == nil || p.DownloadURL != "intel" {
		t.Errorf("amd64 pick = %+v", p)
	}
	if p := v.provider("vmware_desktop", "arm64"); p == nil || p.DownloadURL != "arm" {
		t.Errorf("arm64 pick = %+v", p)
	}
	// Pre-multi-arch boxes report "unknown" and serve as the fallback.
	old := vagrantVersion{Providers: []vagrantProviderEntry{
		{Name: "virtualbox", Architecture: "unknown", DownloadURL: "legacy"},
	}}
	if p := old.provider("virtualbox", "amd64"); p == nil || p.DownloadURL != "legacy" {
		t.Errorf("legacy fallback = %+v", p)
	}
	// No matching arch and no legacy entry: nothing usable.
	if p := v.provider("vmware_desktop", "riscv64"); p != nil {
		t.Errorf("riscv64 should find nothing, got %+v", p)
	}
	if p := v.provider("virtualbox", "amd64"); p != nil {
		t.Errorf("wrong provider name should find nothing, got %+v", p)
	}
}

func TestTalosFactoryURL(t *testing.T) {
	got := talosFactoryURL("abc123", "v1.13.8", "vmware-amd64.ova")
	want := "https://factory.talos.dev/image/abc123/v1.13.8/vmware-amd64.ova"
	if got != want {
		t.Errorf("url = %q, want %q", got, want)
	}
}

func TestGitHubAssetLookup(t *testing.T) {
	rel := &ghRelease{TagName: "v1.0.0", Assets: []ghAsset{
		{Name: "vmware-amd64.ova", Digest: "sha256:beef"},
		{Name: "virtualbox-amd64.ova"},
	}}
	a, err := rel.findAsset("vmware-amd64.ova")
	if err != nil {
		t.Fatal(err)
	}
	if a.sha256() != "beef" {
		t.Errorf("sha256 = %q", a.sha256())
	}
	if _, err := rel.findAsset("metal-amd64.iso"); err == nil ||
		!strings.Contains(err.Error(), "virtualbox-amd64.ova") {
		t.Errorf("missing-asset error should list what exists, got %v", err)
	}
}
