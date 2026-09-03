.PHONY: test race vet test-recovery verify pair

test:
	go test ./...

race:
	go test -race ./cmd/filees ./pkg/activation ./pkg/activity ./pkg/client ./pkg/clientview ./pkg/commit ./pkg/control/v1 ./pkg/deploy ./pkg/ipcserver ./pkg/localrepo ./pkg/onboarding ./pkg/passport ./pkg/provisioning ./pkg/reposupervisor ./pkg/repoworker ./pkg/runtime ./pkg/smtpsubmit ./pkg/watcher ./internal/obsandbox ./internal/servertool ./contracttests

vet:
	go vet ./...

test-recovery:
	./scripts/svn-recovery-smoke.sh

verify: test race vet test-recovery

# The desktop pair, stamped with VERSION + the working-copy revision. Built by
# hand in every session until now, which is why the client called itself
# 0.1.15 from July onwards regardless of what was in it.
pair:
	./packaging/build-pair.sh
