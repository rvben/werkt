// Package buildinfo exposes the identity of the running Werkt binary.
package buildinfo

import "runtime/debug"

// These values are overridden by release and deployment builds using -ldflags.
var (
	Version = "dev"
	Commit  = "unknown"
	BuiltAt = ""
)

// Info is the immutable build identity reported by the CLI and health endpoint.
type Info struct {
	Version string `json:"version"`
	Commit  string `json:"commit"`
	BuiltAt string `json:"builtAt"`
	Dirty   bool   `json:"dirty"`
}

// Current returns linker-provided metadata, supplemented by Go VCS metadata
// for ordinary local builds.
func Current() Info {
	value := Info{Version: Version, Commit: Commit, BuiltAt: BuiltAt}
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return value
	}
	if value.Version == "dev" && info.Main.Version != "" && info.Main.Version != "(devel)" {
		value.Version = info.Main.Version
	}
	for _, setting := range info.Settings {
		switch setting.Key {
		case "vcs.revision":
			if value.Commit == "unknown" && setting.Value != "" {
				value.Commit = setting.Value
			}
		case "vcs.time":
			if value.BuiltAt == "" {
				value.BuiltAt = setting.Value
			}
		case "vcs.modified":
			value.Dirty = setting.Value == "true"
		}
	}
	return value
}
