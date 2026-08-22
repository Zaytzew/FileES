// Package namingpolicy turns a contributor-supplied filename into a
// deterministic repository target name.
//
// Rules match UPLOAD_CHANNEL_CONCEPT.md §3.3 / DROPPER NamingPolicy, without
// the Dropper-specific release suffix. The original name never lands on disk
// as a path; it is only the input to this derivation.
package namingpolicy

import (
	"errors"
	"regexp"
	"strings"
	"unicode"
)

var (
	ErrEmpty       = errors.New("filename is empty")
	ErrPath        = errors.New("filename contains a path separator or parent sequence")
	ErrNoExtension = errors.New("filename has no extension")
	ErrEmptyBase   = errors.New("filename has an empty basename")
	ErrEmptyExt    = errors.New("filename has an empty extension")
	ErrInvalidExt  = errors.New("filename extension is not a safe token")
	ErrEmptyAfter  = errors.New("basename is empty after normalization")
)

var (
	extensionPattern = regexp.MustCompile(`^[a-z0-9]{1,12}$`)
	whitespaceRun    = regexp.MustCompile(`\s+`)
	unsafeRun        = regexp.MustCompile(`[^A-Za-z0-9_-]+`)
	hyphenRun        = regexp.MustCompile(`-+`)
)

var polish = map[rune]rune{
	'ą': 'a', 'ć': 'c', 'ę': 'e', 'ł': 'l', 'ń': 'n', 'ó': 'o', 'ś': 's', 'ż': 'z', 'ź': 'z',
	'Ą': 'A', 'Ć': 'C', 'Ę': 'E', 'Ł': 'L', 'Ń': 'N', 'Ó': 'O', 'Ś': 'S', 'Ż': 'Z', 'Ź': 'Z',
}

// TargetName returns the repository filename derived from a contributor name.
func TargetName(source string) (string, error) {
	base, ext, err := split(source)
	if err != nil {
		return "", err
	}
	base, err = normalizeBase(base)
	if err != nil {
		return "", err
	}
	ext, err = normalizeExt(ext)
	if err != nil {
		return "", err
	}
	return base + "." + ext, nil
}

func split(filename string) (string, string, error) {
	if filename == "" || strings.TrimSpace(filename) == "" {
		return "", "", ErrEmpty
	}
	if strings.ContainsAny(filename, "/\\\x00") || strings.Contains(filename, "..") || filename == "." || filename == ".." {
		return "", "", ErrPath
	}
	filename = strings.TrimSpace(filename)
	pos := strings.LastIndexByte(filename, '.')
	if pos < 0 {
		return "", "", ErrNoExtension
	}
	if pos == 0 {
		return "", "", ErrEmptyBase
	}
	if pos == len(filename)-1 {
		return "", "", ErrEmptyExt
	}
	return filename[:pos], filename[pos+1:], nil
}

func normalizeExt(ext string) (string, error) {
	ext = strings.ToLower(strings.TrimSpace(transliterate(ext)))
	if !extensionPattern.MatchString(ext) {
		return "", ErrInvalidExt
	}
	return ext, nil
}

func normalizeBase(base string) (string, error) {
	base = strings.TrimSpace(base)
	if base == "" {
		return "", ErrEmptyBase
	}
	base = transliterate(base)
	base = strings.ReplaceAll(base, ".", "-")
	base = whitespaceRun.ReplaceAllString(base, "-")
	base = unsafeRun.ReplaceAllString(base, "-")
	base = hyphenRun.ReplaceAllString(base, "-")
	base = strings.Trim(base, "-")
	if base == "" {
		return "", ErrEmptyAfter
	}
	if strings.Contains(base, "..") || strings.ContainsAny(base, "/\\") {
		return "", ErrPath
	}
	return base, nil
}

func transliterate(s string) string {
	return strings.Map(func(r rune) rune {
		if mapped, ok := polish[r]; ok {
			return mapped
		}
		if r == unicode.ReplacementChar {
			return r
		}
		return r
	}, s)
}
