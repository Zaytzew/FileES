package main

import (
	"strings"
	"testing"
)

func TestAuthoritySandboxAllowsPrivateStagingMode(t *testing.T) {
	for _, promise := range strings.Fields(authoritySandboxPromises) {
		if promise == "fattr" {
			return
		}
	}
	t.Fatal("authority sandbox must permit fattr for staging file chmod")
}
