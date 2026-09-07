package passport

import (
	"context"
	"errors"
	"testing"
	"time"

	control "filees/pkg/control/v1"
)

type executionTestTransport func(context.Context, control.Ticket) (control.Result, error)

func (f executionTestTransport) Exchange(ctx context.Context, t control.Ticket) (control.Result, error) {
	return f(ctx, t)
}

func TestExecutionRecoveryDoesNotReplayLockAfterRestart(t *testing.T) {
	for _, loseSettlement := range []bool{false, true} {
		t.Run(map[bool]string{false: "closed", true: "lost-settlement"}[loseSettlement], func(t *testing.T) {
			f := newPendingFixture(t)
			f.backend.FenceAcquisitions = true
			f.cli.denyLock = true
			arms, settles := 0, 0
			var original control.Ticket
			f.backend.Transport = executionTestTransport(func(ctx context.Context, ticket control.Ticket) (control.Result, error) {
				var p control.PassportExecutionPayload
				if err := control.DecodePayload(ticket.Payload, &p); err != nil {
					return control.Result{}, err
				}
				state := "armed"
				switch ticket.Type {
				case control.TicketArmPassportAcquisition:
					arms++
					original = ticket
				case control.TicketSettlePassportAcquisition:
					settles++
					state = "closed"
					var prior control.PassportExecutionPayload
					if err := control.DecodePayload(original.Payload, &prior); err != nil {
						t.Fatal(err)
					}
					if original.OperationID != ticket.OperationID || original.RequestID != ticket.RequestID || prior != p {
						t.Fatal("settlement changed attempt")
					}
					if loseSettlement && settles == 1 {
						return control.Result{}, errors.New("lost reply")
					}
				default:
					t.Fatalf("unexpected ticket %s", ticket.Type)
				}
				return control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.PassportExecutionResult{RepoID: p.RepoID, Path: p.Path, Comment: p.Comment, State: state}, f.now)
			})
			m := f.open(t)
			if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err == nil {
				t.Fatal("failed acquire succeeded")
			}
			m = f.open(t)
			if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err == nil {
				t.Fatal("settlement must end this acquisition with refusal")
			}
			if loseSettlement {
				p := m.passports[f.path]
				if p.Pending == nil || p.Pending.Stage != "settling" {
					t.Fatal("closing direction not durable")
				}
				f.now = f.now.Add(-time.Hour)
				m = f.open(t)
				if _, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm); err == nil {
					t.Fatal("settlement unexpectedly acquired")
				}
			}
			if len(m.passports) != 0 || arms != 1 || f.cli.locks != 1 || f.cli.unlocks != 0 {
				t.Fatalf("unsafe recovery: passports=%v arms=%d locks=%d unlocks=%d", m.passports, arms, f.cli.locks, f.cli.unlocks)
			}
		})
	}
}

func TestExecutionRecoveryRejectsUnboundClosedReceipt(t *testing.T) {
	f := newPendingFixture(t)
	f.backend.FenceAcquisitions = true
	f.cli.denyLock = true
	f.backend.Transport = executionTestTransport(func(ctx context.Context, ticket control.Ticket) (control.Result, error) {
		var p control.PassportExecutionPayload
		if err := control.DecodePayload(ticket.Payload, &p); err != nil {
			return control.Result{}, err
		}
		state := "armed"
		if ticket.Type == control.TicketSettlePassportAcquisition {
			state = "closed"
			p.Path = "other.txt"
		}
		return control.NewSuccessResult(ticket.OperationID, ticket.RequestID, ticket.Type, control.PassportExecutionResult{RepoID: p.RepoID, Path: p.Path, Comment: p.Comment, State: state}, f.now)
	})
	m := f.open(t)
	_, _, _ = m.Acquire(t.Context(), []string{f.path}, f.realm)
	m = f.open(t)
	_, _, err := m.Acquire(t.Context(), []string{f.path}, f.realm)
	if err == nil || len(m.passports) != 1 || m.passports[f.path].Pending.Stage != "settling" || f.cli.locks != 1 {
		t.Fatal("unbound receipt removed fence or replayed lock")
	}
}
