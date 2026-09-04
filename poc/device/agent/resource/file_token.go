package resource

import (
	"regexp"
	"strings"
)

func SanitizeFileToken(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return "unknown"
	}
	replacer := regexp.MustCompile(`[^a-zA-Z0-9_-]+`)
	cleaned := replacer.ReplaceAllString(value, "-")
	cleaned = strings.Trim(cleaned, "-")
	if cleaned == "" {
		return "unknown"
	}
	return cleaned
}
