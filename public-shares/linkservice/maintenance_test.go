package linkservice

import (
	"os"
	"strings"
	"testing"
	"time"

	"filees/public-shares/web"
)

func TestMaintenanceConfigAndHandlerShareTheSameStore(t *testing.T) {
	for _, interval := range []string{"5m", "0s", "nonsense"} {
		body := `{"schema":"filees.public-links/v1","fastcgi":{"network":"unix","address":"@ROOT@/fcgi.sock"},"backchannel":{"network":"unix","address":"@ROOT@/authority.sock"},"visit_key_file":"@KEY@","cache":{"enabled":true,"root":"@ROOT@/cache","max_size":1024,"cleanup_interval":"INTERVAL"}}`
		path := writeConfigFixture(t, strings.ReplaceAll(body, "INTERVAL", interval))
		r, err := LoadForCheck(path)
		if interval != "5m" {
			if err == nil {
				t.Fatal("invalid interval accepted", interval)
			}
			continue
		}
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(r.Config.Cache.Root); !os.IsNotExist(err) {
			t.Fatal("read-only check created cache root")
		}
		r, err = Load(path)
		if err != nil {
			t.Fatal(err)
		}
		if r.CleanupInterval != 5*time.Minute || r.Store == nil || r.Handler().(web.Handler).Cache != r.Store || r.Handler().(web.Handler).Cache != r.Store {
			t.Fatal("maintenance and requests do not share Store")
		}
	}
}
