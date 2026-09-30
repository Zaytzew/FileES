package main

import (
	"filees/pkg/serverconfig"
	"testing"
)

func TestListenRejectsTCPWithoutConfigurationLoader(t *testing.T) {
	for _, network := range []string{"tcp", "tcp4", "", "unixpacket"} {
		ln, cleanup, err := listen(serverconfig.PublicSharesFile{BackchannelNetwork: network, BackchannelAddress: "127.0.0.1:0"})
		if err == nil || ln != nil || cleanup != nil {
			t.Fatalf("network %q accepted", network)
		}
	}
}
