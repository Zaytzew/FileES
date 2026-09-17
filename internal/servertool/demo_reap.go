package servertool

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"time"

	"filees/pkg/activation"
	"filees/pkg/onboarding"
	"filees/pkg/repoworker"
	"filees/pkg/serverconfig"
	"github.com/google/uuid"
)

// demoRealmOperationID derives the single removal operation of one expired
// demo realm, so every later reap finds and resumes the same journal instead
// of starting another.
func demoRealmOperationID(realmID string) string {
	return uuid.NewSHA1(uuid.NameSpaceOID, []byte("filees-demo-realm-expiry:"+realmID)).String()
}

type demoReapResult struct {
	Schema  string   `json:"schema"`
	Status  string   `json:"status"`
	Expired []string `json:"expired_realms"`
	Removed []string `json:"removed_realms"`
}

// reapExpiredDemoRealms deletes every demo realm whose TTL has passed since its
// activation: owned repositories without archives, then every credential.
// It is idempotent; a completed realm is skipped and an interrupted one is
// resumed from its journal.
func reapExpiredDemoRealms(ctx context.Context, runtime realmRemovalRuntime, policy serverconfig.DemoPolicy, activationConfig activation.Config, now time.Time) (demoReapResult, error) {
	result := demoReapResult{Schema: "filees.demo-reap-result/v1", Status: "ok", Expired: []string{}, Removed: []string{}}
	if !policy.Enabled {
		return result, errors.New("this server has no demo section")
	}
	activations, err := runtime.manager.RealmActivations()
	if err != nil {
		return result, err
	}
	for realmID, activatedAt := range activations {
		if policy.Expired(activatedAt, now) {
			result.Expired = append(result.Expired, realmID)
		}
	}
	sort.Strings(result.Expired)
	if len(result.Expired) == 0 {
		return result, nil
	}
	err = withServiceWorkingCopy(ctx, activationConfig, func() error {
		for _, realmID := range result.Expired {
			removed, err := reapDemoRealm(ctx, runtime, realmID)
			if err != nil {
				return fmt.Errorf("demo realm %s: %w", realmID, err)
			}
			if removed {
				result.Removed = append(result.Removed, realmID)
			}
		}
		return nil
	})
	return result, err
}

func reapDemoRealm(ctx context.Context, runtime realmRemovalRuntime, realmID string) (bool, error) {
	operationID := demoRealmOperationID(realmID)
	record, err := runtime.store.Load(operationID)
	switch {
	case err == nil && record.State == repoworker.RealmRemovalCompleted:
		return false, nil
	case errors.Is(err, os.ErrNotExist):
		scope, err := runtime.publisher.SnapshotRealmScope(realmID)
		if err != nil {
			return false, err
		}
		if scope.ClientIDs, err = runtime.manager.ActiveClientsInRealm(realmID); err != nil {
			return false, err
		}
		sort.Strings(scope.ClientIDs)
		sort.Strings(scope.OwnedRepoIDs)
		sort.Strings(scope.ForeignGrantRepoIDs)
		if record, err = runtime.store.BeginPolicyRemoval(operationID, realmID, scope); err != nil {
			return false, err
		}
	case err != nil:
		return false, err
	}
	if err := runtime.executor.Execute(ctx, record); err != nil {
		return false, err
	}
	// Demo channels only ever listed content; with the repositories gone they
	// describe nothing, so they go with the realm rather than as an erasure.
	if runtime.channels != nil {
		if _, err := runtime.channels.DeleteRealm(realmID); err != nil {
			return false, err
		}
	}
	return true, nil
}

func runAdminDemoReap(configPath string, args []string, stdout, stderr io.Writer) int {
	if len(args) != 0 {
		fmt.Fprintln(stderr, "usage: filees-admin [-config path] demo reap")
		return ExitUsage
	}
	_, config, err := openFiles(configPath, toolAccess{
		name: "filees-admin/demo-reap", areas: onboarding.AreaOperations, write: true,
		needOTP: true, needActivation: true, needRepoResults: true,
		needRepositoryData: true, needSVN: true, needPublicShareState: true, publicShareStateWrite: true,
	})
	if err != nil {
		report(stderr, "filees-admin demo reap config", err)
		return ExitConfig
	}
	if !config.Demo.Enabled {
		report(stderr, "filees-admin demo reap", errors.New("this server has no demo section"))
		return ExitConfig
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	var result demoReapResult
	err = repoworker.WithFileLock(filepath.Join(config.Repositories.ResultsRoot, ".worker.lock"), func() error {
		runtime, err := newRealmRemovalRuntime(config)
		if err != nil {
			return err
		}
		result, err = reapExpiredDemoRealms(ctx, runtime, config.Demo, config.Activation, time.Now())
		return err
	})
	if err != nil {
		report(stderr, "filees-admin demo reap", err)
		return ExitTempFail
	}
	if err := writeJSON(stdout, result); err != nil {
		return ExitSoftware
	}
	return ExitOK
}
