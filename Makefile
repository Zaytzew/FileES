.PHONY: test race vet test-recovery verify

test:
	go test ./...

race:
	go test -race ./pkg/client ./pkg/commit ./pkg/runtime ./contracttests

vet:
	go vet ./...

test-recovery:
	./scripts/svn-recovery-smoke.sh

verify: test race vet test-recovery
