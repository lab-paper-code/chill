package defaults

import "strings"

// String trims value and falls back when the trimmed value is empty.
func String(value, fallback string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return fallback
	}
	return value
}
