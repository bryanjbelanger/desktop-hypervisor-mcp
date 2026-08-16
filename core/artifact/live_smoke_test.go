//go:build livesmoke

package artifact

import (
	"testing"

	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider"
)

func TestLiveTalosDryRun(t *testing.T) {
	for _, k := range []provider.Kind{provider.KindVirtualBox, provider.KindFusion} {
		r, err := Resolve("talos", desc(k, "amd64"))
		if err != nil {
			t.Fatalf("%s resolve: %v", k, err)
		}
		res, err := Fetch(t.Context(), r, "", t.TempDir(), true)
		if err != nil {
			t.Fatalf("%s fetch dry-run: %v", k, err)
		}
		t.Logf("%s:\n%s", k, res.Summary)
	}
}
