// Package buildinfo resolves the identity of the running gomcdc binary from
// Go build metadata. Release builds installed from a module version retain
// that version; local builds are explicitly identified as development builds.
package buildinfo

import (
	"runtime/debug"
	"strings"
)

// Version returns the release or development identity of the running binary.
func Version() string {
	info, ok := debug.ReadBuildInfo()
	return resolve(info, ok)
}

func resolve(info *debug.BuildInfo, ok bool) string {
	if !ok || info == nil {
		return "devel"
	}
	if version := strings.TrimSpace(info.Main.Version); version != "" && version != "(devel)" {
		return version
	}

	revision := ""
	modified := false
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			revision = strings.TrimSpace(setting.Value)
		case "vcs.modified":
			modified = setting.Value == "true"
		}
	}
	version := "devel"
	if revision != "" {
		if len(revision) > 12 {
			revision = revision[:12]
		}
		version += "-" + revision
	}
	if modified {
		version += "-dirty"
	}
	return version
}
