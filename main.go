// Desktop Hypervisor MCP — one provider-neutral server over VirtualBox,
// VMware Fusion, and VMware Workstation.
//
// Status: foundation only. Provider discovery and artifact resolution are
// implemented; the neutral lifecycle/snapshot/guest/network tools are not yet
// ported from the two predecessor servers. See PLAN.md.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/bryanjbelanger/desktop-hypervisor-mcp/core/artifact"
	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider"
	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider/virtualbox"
	"github.com/bryanjbelanger/desktop-hypervisor-mcp/provider/vmware"
)

var detectors = []provider.Detector{virtualbox.Detector{}, vmware.Detector{}}

// discover probes every adapter family once.
func discover(ctx context.Context) []provider.Descriptor {
	var all []provider.Descriptor
	for _, d := range detectors {
		all = append(all, d.Detect(ctx)...)
	}
	return all
}

func ready(ds []provider.Descriptor) []provider.Descriptor {
	var r []provider.Descriptor
	for _, d := range ds {
		if d.Status == provider.StatusReady {
			r = append(r, d)
		}
	}
	return r
}

type providerIn struct {
	Action string `json:"action" jsonschema:"list (all providers and their capabilities) or resolve (pick an artifact for a provider)"`
	ID     string `json:"id,omitempty" jsonschema:"provider id from list; defaults to the only ready provider"`
	Image  string `json:"image,omitempty" jsonschema:"image name for resolve, e.g. talos, talos-iso, ubuntu, or vagrant:org/box"`
}

func text(s string) *mcp.CallToolResult {
	return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: s}}}
}

func providerTool(ctx context.Context, _ *mcp.CallToolRequest, in providerIn) (*mcp.CallToolResult, any, error) {
	ds := discover(ctx)

	switch in.Action {
	case "", "list":
		b, err := json.MarshalIndent(map[string]any{"providers": ds}, "", "  ")
		if err != nil {
			return nil, nil, err
		}
		return text(string(b)), nil, nil

	case "resolve":
		if in.Image == "" {
			return text("resolve requires an image name"), nil, nil
		}
		d, err := selectProvider(ds, in.ID)
		if err != nil {
			return text(err.Error()), nil, nil
		}
		r, err := artifact.Resolve(in.Image, *d)
		if err != nil {
			return text(fmt.Sprintf("cannot resolve %q for %s: %v", in.Image, d.ID, err)), nil, nil
		}
		b, _ := json.MarshalIndent(r, "", "  ")
		return text(string(b)), nil, nil
	}
	return text("unknown action " + in.Action), nil, nil
}

// selectProvider resolves an explicit id, or auto-selects when exactly one
// provider is ready. Ambiguity is surfaced to the caller rather than guessed.
func selectProvider(ds []provider.Descriptor, id string) (*provider.Descriptor, error) {
	if id != "" {
		for i := range ds {
			if ds[i].ID == id {
				if ds[i].Status != provider.StatusReady {
					return nil, fmt.Errorf("provider %s is %s: %s", id, ds[i].Status, ds[i].Remediation)
				}
				return &ds[i], nil
			}
		}
		return nil, fmt.Errorf("no such provider %q", id)
	}
	r := ready(ds)
	switch len(r) {
	case 0:
		return nil, fmt.Errorf("no hypervisor is ready on this host")
	case 1:
		return &r[0], nil
	default:
		var names []string
		for _, d := range r {
			names = append(names, d.ID)
		}
		return nil, fmt.Errorf("several providers are ready (%v) — pass id to choose", names)
	}
}

// instructions are generated from what is actually present, so a host with a
// single hypervisor never pays context for the other.
func instructions(ds []provider.Descriptor) string {
	s := "Desktop hypervisor operations across VirtualBox and VMware (Fusion/Workstation).\n" +
		"Call provider action=list first: it reports which hypervisors are installed, " +
		"their capabilities, and remediation for any that are not ready.\n"
	r := ready(ds)
	switch len(r) {
	case 0:
		s += "No hypervisor is currently ready on this host; list reports how to fix that.\n"
	case 1:
		s += fmt.Sprintf("One provider is ready (%s); it is selected automatically.\n", r[0].ID)
	default:
		s += "Several providers are ready — ask the user which to use, then pass its id.\n"
	}
	return s
}

func main() {
	ctx := context.Background()
	ds := discover(ctx)

	server := mcp.NewServer(
		&mcp.Implementation{Name: "desktop-hypervisor-mcp", Version: "0.1.0"},
		&mcp.ServerOptions{Instructions: instructions(ds)},
	)
	mcp.AddTool(server, &mcp.Tool{
		Name: "provider",
		Description: "Discover desktop hypervisors on this host and resolve OS images to a " +
			"provider-appropriate artifact. action=list reports every provider with status, " +
			"capabilities, accepted formats and remediation. action=resolve picks the correct " +
			"artifact for an image given the selected provider and host architecture.",
	}, providerTool)

	if err := server.Run(ctx, &mcp.StdioTransport{}); err != nil {
		log.Fatal(err)
	}
	os.Exit(0)
}
