package magpie

import (
	"os"
	"strings"
)

// SecretFromEnv reads NAME, or the contents of NAME_FILE when NAME is empty.
// Docker / Compose secret mounts use the _FILE convention. The value is never
// logged. Prefer the environment variable in local use so the path cannot leak
// into shell history next to a flag.
func SecretFromEnv(name string) string {
	if value := strings.TrimSpace(os.Getenv(name)); value != "" {
		return value
	}
	path := strings.TrimSpace(os.Getenv(name + "_FILE"))
	if path == "" {
		return ""
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(data))
}
