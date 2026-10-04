package clientupdate

import "testing"

func TestStoreInformationComesFromRunningBuild(t *testing.T) {
	for _, tc := range []struct{ build, want string }{{"0.1.18+r1882", "0.1.18.1882"}, {"0.1.18+r1903", "0.1.18.1903"}, {"0.1.18.1882", "0.1.18.1882"}} {
		info, err := (StoreInfo{CurrentVersion: tc.build}).Status(t.Context())
		if err != nil || info.State != "store" || info.Channel != "store" || info.CurrentVersion != tc.want || info.AvailableVersion != "" || info.ReleaseID != "" || info.Summary != "" || info.RestartRequired {
			t.Fatalf("info=%+v err=%v", info, err)
		}
	}
}
