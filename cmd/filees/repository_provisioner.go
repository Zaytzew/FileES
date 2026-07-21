package main

import (
	"context"
	"errors"
	"sync"
	"time"

	"filees/pkg/client"
	"filees/pkg/clientprofile"
	"filees/pkg/config"
	"filees/pkg/controlclient"
	"filees/pkg/localrepo"
	"filees/pkg/provisioning"
	"filees/pkg/talk"
)

type daemonProvisioner struct {
	local        *localrepo.Store
	provisioning *provisioning.Store
	mu           sync.RWMutex
	profiles     map[string]clientprofile.Profile
	queue        chan string
	attachments  chan<- provisionedAttachment
}

type provisionedAttachment struct{ Repo config.Repo }

func newDaemonProvisioner(local *localrepo.Store, store *provisioning.Store, profiles []clientprofile.Profile) *daemonProvisioner {
	p := &daemonProvisioner{local: local, provisioning: store, profiles: make(map[string]clientprofile.Profile), queue: make(chan string, 32)}
	for _, profile := range profiles {
		p.profiles[profile.ServerID] = profile
	}
	return p
}

func (p *daemonProvisioner) AddProfile(profile clientprofile.Profile) {
	p.mu.Lock()
	p.profiles[profile.ServerID] = profile
	p.mu.Unlock()
}

func (p *daemonProvisioner) ClientID(serverID string) string {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.profiles[serverID].ClientID
}

func (p *daemonProvisioner) Enqueue(operationID string) {
	select {
	case p.queue <- operationID:
	default:
		talk.With("provisioning").Warnf("queue full; operation %s remains durable for restart", operationID)
	}
}

func (p *daemonProvisioner) Run(ctx context.Context) {
	if operations, err := p.provisioning.List(); err != nil {
		talk.With("provisioning").Errorf("restore operations: %v", err)
	} else {
		for _, operation := range operations {
			if operation.State == provisioning.StateActive {
				p.publishAttachment(ctx, operation)
			} else {
				p.Enqueue(operation.OperationID)
			}
		}
	}
	for {
		select {
		case <-ctx.Done():
			return
		case operationID := <-p.queue:
			p.runOne(ctx, operationID)
		}
	}
}

func (p *daemonProvisioner) runOne(ctx context.Context, operationID string) {
	record, ok := p.local.Get(operationID)
	if !ok {
		talk.With("provisioning").Errorf("local lifecycle record %s is missing", operationID)
		return
	}
	p.mu.RLock()
	profile, ok := p.profiles[record.ServerID]
	p.mu.RUnlock()
	if !ok {
		_, _ = p.local.MarkError(operationID, errors.New("activated client profile is unavailable"))
		return
	}
	controlTransport, err := controlclient.New(controlclient.Config{Address: profile.Address, Port: profile.SSHPort, IdentityFile: profile.IdentityFile, KnownHosts: profile.KnownHosts, Timeout: 30 * time.Second})
	if err != nil {
		_, _ = p.local.MarkError(operationID, err)
		return
	}
	svn := client.New(client.Options{SvnPath: "svn", Timeout: 30 * time.Minute, LogScope: "svn:provisioning:" + operationID, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, SSHPort: profile.SSHPort, SSHHostName: profile.Address})
	orchestrator := provisioning.Orchestrator{Store: p.provisioning, Control: controlTransport, SVN: svn, Limits: provisioning.ImportLimits{MaxBatchFiles: 100, MaxBatchBytes: 512 << 20}}
	operation, err := orchestrator.RunCreate(ctx, operationID)
	if err != nil {
		_, _ = p.local.MarkError(operationID, err)
		talk.With("provisioning:"+operationID).Warnf("create failed: %v", err)
		return
	}
	if _, err := p.local.MarkAttached(operationID, operation.RepoID); err != nil {
		talk.With("provisioning:"+operationID).Errorf("publish local attachment: %v", err)
		return
	}
	p.publishAttachment(ctx, operation)
}

func (p *daemonProvisioner) publishAttachment(ctx context.Context, operation provisioning.Operation) {
	if p.attachments == nil {
		return
	}
	record, ok := p.local.Get(operation.OperationID)
	if !ok || record.State != localrepo.StateAttached || record.RepoID != operation.RepoID {
		return
	}
	p.mu.RLock()
	profile, ok := p.profiles[record.ServerID]
	p.mu.RUnlock()
	if ok {
		repo := config.Repo{ID: operation.RepoID, RepoURL: operation.RepoURL, LocalPath: operation.LocalPath, SSHIdentityFile: profile.IdentityFile, SSHKnownHosts: profile.KnownHosts, ServerID: profile.ServerID, ServerDisplayName: profile.DisplayName, ClientRole: "normal", Access: "rw"}
		select {
		case p.attachments <- provisionedAttachment{Repo: repo}:
		case <-ctx.Done():
		}
	}
}
