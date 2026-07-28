// Package realmalias validates the immutable, human-facing identity of a
// FileES realm. It deliberately accepts a small ASCII subset: aliases are
// displayed in native UI and may later be used as routing identifiers.
package realmalias

import (
	"errors"
	"strings"
)

const (
	MinLength = 3
	MaxLength = 32
)

// Normalize validates value and returns its canonical, case-insensitive form.
// The intentionally narrow alphabet prevents shell, markup and Unicode
// confusable injection at every later display or transport boundary.
func Normalize(value string) (string, error) {
	value = strings.ToLower(strings.TrimSpace(value))
	if len(value) < MinLength || len(value) > MaxLength {
		return "", errors.New("alias must contain 3 to 32 characters")
	}
	if !letterOrDigit(value[0]) || !letterOrDigit(value[len(value)-1]) {
		return "", errors.New("alias must start and end with a letter or digit")
	}
	for i := 0; i < len(value); i++ {
		if letterOrDigit(value[i]) || value[i] == '-' || value[i] == '_' || value[i] == '.' {
			continue
		}
		return "", errors.New("alias may contain only letters, digits, '.', '_' and '-'")
	}
	return value, nil
}

func letterOrDigit(ch byte) bool {
	return ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9'
}
