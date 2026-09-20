//go:build linux

package main

import (
	"reflect"
	"testing"
)

func TestLinuxSystemLanguages(t *testing.T) {
	for _, tc := range []struct {
		name string
		env  map[string]string
		file string
		want []string
	}{
		{"session", map[string]string{"LANG": "pl_PL.UTF-8"}, `LANG="en_US.UTF-8"`, []string{"pl-PL"}},
		{"system file", nil, "# system\nLANG=\"pl_PL.UTF-8\"\n", []string{"pl-PL"}},
		{"messages", map[string]string{"LANG": "en_US.UTF-8", "LC_MESSAGES": "de_DE@euro"}, "", []string{"de-DE"}},
		{"all", map[string]string{"LANG": "pl_PL", "LC_MESSAGES": "de_DE", "LC_ALL": "fr_FR.UTF-8"}, "", []string{"fr-FR"}},
		{"GNU language", map[string]string{"LANG": "pl_PL.UTF-8", "LANGUAGE": "es_ES:en"}, "", []string{"es-ES", "en"}},
		{"C", map[string]string{"LANG": "pl_PL", "LC_ALL": "C.UTF-8", "LANGUAGE": "pl"}, "", []string{"en"}},
		{"POSIX", map[string]string{"LANG": "POSIX"}, "", []string{"en"}},
		{"unknown", nil, "", nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := linuxSystemLanguages(func(key string) string { return tc.env[key] }, tc.file)
			if !reflect.DeepEqual(got, tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
		})
	}
}
