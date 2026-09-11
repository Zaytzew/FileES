package errcat

import "strings"

// Diagnostic returns the English log sentence for key.
func Diagnostic(key string) string {
	if spec, ok := ByKey(Key(key)); ok && spec.Diagnostic != "" {
		return spec.Diagnostic
	}
	return "Unexpected error"
}

// KnownKey reports whether the dictionary has this message key.
func KnownKey(key string) bool {
	_, ok := ByKey(Key(strings.TrimSpace(key)))
	return ok
}
