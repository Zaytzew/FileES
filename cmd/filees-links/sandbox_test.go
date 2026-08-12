package main

import (
	"strings"
	"testing"
)

func TestLinksSandboxAllowsPrivateCacheMode(t *testing.T) {
	for _, promise := range strings.Fields(linksSandboxPromises) {
		if promise == "fattr" {
			return
		}
	}
	t.Fatal("links sandbox must permit fattr for cache file chmod")
}
