package provider

import (
	"context"
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// CommandTimeout bounds any single provider CLI invocation. Long operations
// (imports, exports, installs) fit; hangs do not.
const CommandTimeout = 600 * time.Second

// RunCmd runs a host command and returns trimmed output. Both streams are
// considered: VBoxManage reports progress on stderr, vmrun errors on stdout.
// An error with empty streams still surfaces the OS error — losing it makes
// failures undebuggable.
func RunCmd(ctx context.Context, bin string, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, CommandTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Env = ChildEnv()
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	name := filepath.Base(bin)
	if ctx.Err() == context.DeadlineExceeded {
		return "", fmt.Errorf("%s timed out after %s", name, CommandTimeout)
	}
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return "", fmt.Errorf("%s failed: %s", name, msg)
	}
	out := strings.TrimSpace(stdout.String())
	if out == "" {
		out = strings.TrimSpace(stderr.String())
	}
	if out == "" {
		out = "(ok)"
	}
	return out, nil
}
