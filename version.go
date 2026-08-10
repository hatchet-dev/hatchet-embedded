package embed

import (
	"fmt"
	"runtime/debug"
	"strings"
)

const hatchetModulePath = "github.com/hatchet-dev/hatchet"

func resolveVersion() (string, error) {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		return "", fmt.Errorf("could not read build info to resolve the Hatchet engine version")
	}

	for _, d := range info.Deps {
		if d.Path != hatchetModulePath {
			continue
		}
		if d.Replace != nil && isUsableVersion(d.Replace.Version) {
			return d.Replace.Version, nil
		}
		if isUsableVersion(d.Version) {
			return d.Version, nil
		}
	}

	return "", fmt.Errorf("could not resolve the %s dependency version from build info", hatchetModulePath)
}

func isUsableVersion(v string) bool {
	return strings.HasPrefix(v, "v") && v != "(devel)"
}
