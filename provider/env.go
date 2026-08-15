package provider

import (
	"os"
	"os/user"
	"runtime"
	"strings"
)

// ChildEnv returns the environment for provider CLI children, with
// USER/LOGNAME/HOME normalized to the effective uid. Under vmrun guest exec,
// cron, CI, and service managers these variables can be missing or name a
// different user; VBoxManage then warns ("Environment variable LOGNAME or
// USER does not correspond to effective user id") and, worse, may address an
// inconsistent per-user machine registry — createvm succeeds while the
// following modifyvm fails to lock the machine it just created. A nil return
// means the inherited environment is already consistent (exec.Cmd treats nil
// Env as inherit). The server's own environment is never mutated.
func ChildEnv() []string {
	if runtime.GOOS == "windows" {
		return nil
	}
	u, err := user.Current()
	if err != nil || u.Username == "" {
		return nil
	}
	return normalizeEnv(os.Environ(), u.Username, u.HomeDir)
}

// normalizeEnv returns nil when environ already carries USER and LOGNAME
// equal to username and a non-empty HOME. Otherwise it returns a copy with
// USER and LOGNAME set to username and HOME filled from home only when it is
// missing or empty — an explicitly set HOME is respected, since it is a
// legitimate way to relocate VirtualBox's per-user configuration.
func normalizeEnv(environ []string, username, home string) []string {
	var userVal, lognameVal, homeVal string
	for _, kv := range environ {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		switch k {
		case "USER":
			userVal = v
		case "LOGNAME":
			lognameVal = v
		case "HOME":
			homeVal = v
		}
	}
	if userVal == username && lognameVal == username && homeVal != "" {
		return nil
	}
	setHome := homeVal == "" && home != ""
	out := make([]string, 0, len(environ)+3)
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if k == "USER" || k == "LOGNAME" || (k == "HOME" && setHome) {
			continue
		}
		out = append(out, kv)
	}
	out = append(out, "USER="+username, "LOGNAME="+username)
	if setHome {
		out = append(out, "HOME="+home)
	}
	return out
}
