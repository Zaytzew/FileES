.PHONY: test race vet test-recovery verify

test:
	go test ./...

race:
	go test -race ./pkg/activation ./pkg/client ./pkg/clientview ./pkg/commit ./pkg/control/v1 ./pkg/deploy ./pkg/onboarding ./pkg/passport ./pkg/provisioning ./pkg/runtime ./pkg/smtpsubmit ./pkg/watcher ./internal/obsandbox ./internal/servertool ./contracttests

vet:
	go vet ./...

test-recovery:
	./scripts/svn-recovery-smoke.sh

verify: test race vet test-recovery
