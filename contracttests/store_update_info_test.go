package contracttests

import (
	"context"
	"testing"

	"filees/internal/clientupdate"
	contract "filees/pkg/contract/v1"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

func TestStoreDistributionStatusSurvivesRestartWithoutFileUpdater(t *testing.T) {
	for _, version := range []string{"0.1.18+r1882", "0.1.18+r1903"} {
		t.Run(version, func(t *testing.T) {
			sock := testSocketPath(t)
			server := ipcserver.New(sock)
			server.SetUpdateStatusSource(clientupdate.StoreInfo{CurrentVersion: version})
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if err := server.Start(ctx); err != nil {
				t.Fatal(err)
			}
			for range 2 {
				client := ipcclient.New(sock, "store-info-test")
				hello, err := client.Hello(ctx)
				if err != nil {
					t.Fatal(err)
				}
				has := func(want string) bool {
					for _, cap := range hello.Capabilities {
						if cap == want {
							return true
						}
					}
					return false
				}
				if !has(contract.CapUpdateStatus) || has(contract.CapUpdatePlan) || has(contract.CapUpdateApply) {
					t.Fatal(hello.Capabilities)
				}
				status, err := client.SystemStatus(ctx)
				if err != nil {
					t.Fatal(err)
				}
				info := status.Update
				if info == nil || info.State != "store" || info.Channel != "store" || info.CurrentVersion == "" || info.AvailableVersion != "" || info.ReleaseID != "" || info.RestartRequired {
					t.Fatalf("info=%+v", info)
				}
				direct, err := client.UpdateStatus(ctx)
				if err != nil || direct.CurrentVersion != info.CurrentVersion {
					t.Fatalf("direct=%+v err=%v", direct, err)
				}
				if _, err := client.UpdatePlan(ctx); err == nil {
					t.Fatal("Store advertised file planning")
				}
				if _, err := client.UpdateApply(ctx); err == nil {
					t.Fatal("Store allowed file replacement")
				}
			}
		})
	}
}
