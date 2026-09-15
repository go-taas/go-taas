package config

import (
	"os"
	"strings"
)

// LoadDotEnv loads KEY=VALUE pairs from the given file into the process
// environment. Existing environment variables take precedence, so an .env
// file never overrides what the operator explicitly exported. A missing
// file is not an error.
func LoadDotEnv(path string) error {
	content, err := os.ReadFile(path) // #nosec G304 -- caller-provided path
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return err
	}

	for _, line := range strings.Split(string(content), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, found := strings.Cut(line, "=")
		if !found {
			continue
		}
		key = strings.TrimSpace(key)
		value = strings.TrimSpace(value)
		// Strip one pair of surrounding quotes.
		if len(value) >= 2 &&
			((value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'')) {
			value = value[1 : len(value)-1]
		}
		if key == "" {
			continue
		}
		if _, exists := os.LookupEnv(key); !exists {
			_ = os.Setenv(key, value)
		}
	}
	return nil
}
