//go:build linux

package main

import (
	"os"
	"strings"
)

func systemLanguages() []string {
	fallback, _ := os.ReadFile("/etc/locale.conf")
	return linuxSystemLanguages(os.Getenv, string(fallback))
}

func linuxSystemLanguages(getenv func(string) string, fallback string) []string {
	values := map[string]string{}
	for _, line := range strings.Split(fallback, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if ok {
			values[key] = strings.Trim(strings.TrimSpace(value), `"'`)
		}
	}
	get := func(key string) string {
		if value := strings.TrimSpace(getenv(key)); value != "" {
			return value
		}
		return values[key]
	}
	category := get("LC_ALL")
	if category == "" {
		category = get("LC_MESSAGES")
	}
	if category == "" {
		category = get("LANG")
	}
	if category == "" {
		return nil
	}
	normalize := func(value string) string {
		value = strings.SplitN(value, ".", 2)[0]
		value = strings.SplitN(value, "@", 2)[0]
		return strings.ReplaceAll(value, "_", "-")
	}
	if c := normalize(category); c == "C" || c == "POSIX" {
		return []string{"en"}
	}
	// GNU LANGUAGE selects message language within a non-C locale.
	if language := get("LANGUAGE"); language != "" {
		var out []string
		for _, value := range strings.Split(language, ":") {
			if value != "" {
				out = append(out, normalize(value))
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []string{normalize(category)}
}
