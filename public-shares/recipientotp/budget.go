package recipientotp

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"time"

	"filees/internal/durable"
	"github.com/google/uuid"
)

// ErrSendBudget means the code was not queued again: the invitation or the
// channel used up its queue attempts in the last day. An active cooldown is
// not an error. The web page answers
// the same way as for a sent code, so the budget reveals nothing about the
// mailbox.
var ErrSendBudget = errors.New("recipient OTP send budget exhausted")

const (
	budgetSchema = "filees.public-share-recipient-otp-budget/v1"
	budgetName   = "send-budget.json"
	budgetWindow = 24 * time.Hour

	// A bounded journal must round-trip even at the largest allowed setting.
	// Each canonical entry is < 160 bytes; allow 16 MiB including the envelope.
	maxBudgetEntries = 100000
	maxBudgetBytes   = 16 << 20

	// Zero in server.json keeps these defaults.
	DefaultSendsPerInvitation = 5
	DefaultSendsPerChannel    = 50
)

// laterCooldowns is the pause after the n-th send of one invitation inside the
// window: 30 s after the first, 2 min after the second, 10 min from the third.
// The first step is Service.Cooldown, so existing configuration keeps working.
var laterCooldowns = []time.Duration{2 * time.Minute, 10 * time.Minute}

type sendBudget struct {
	Schema    string        `json:"schema"`
	ChannelID string        `json:"channel_id"`
	Sends     []budgetEntry `json:"sends"`
}

type budgetEntry struct {
	InvitationHash string    `json:"invitation_sha256"`
	At             time.Time `json:"at"`
}

// The budget lives beside the per-invitation OTP state but is independent of
// its epoch: a new epoch or a restart must not reset what was already sent.
func (s Service) budgetPath(channelID string) string {
	return filepath.Join(s.Root, channelID, budgetName)
}

func (s Service) loadBudget(channelID string) (sendBudget, error) {
	root, err := os.OpenRoot(filepath.Dir(s.budgetPath(channelID)))
	if errors.Is(err, os.ErrNotExist) {
		return sendBudget{Schema: budgetSchema, ChannelID: channelID}, nil
	}
	if err != nil {
		return sendBudget{}, err
	}
	defer root.Close()
	return readBudget(root, channelID)
}

func readBudget(root *os.Root, channelID string) (sendBudget, error) {
	info, err := root.Lstat(budgetName)
	if errors.Is(err, os.ErrNotExist) {
		return sendBudget{Schema: budgetSchema, ChannelID: channelID}, nil
	}
	if err != nil {
		return sendBudget{}, err
	}
	if !info.Mode().IsRegular() || info.Size() > maxBudgetBytes {
		return sendBudget{}, errors.New("stored recipient OTP budget is not a bounded regular file")
	}
	file, err := root.Open(budgetName)
	if err != nil {
		return sendBudget{}, err
	}
	defer file.Close()
	raw, err := io.ReadAll(io.LimitReader(file, maxBudgetBytes+1))
	if err != nil {
		return sendBudget{}, err
	}
	var budget sendBudget
	if len(raw) > maxBudgetBytes || json.Unmarshal(raw, &budget) != nil || budget.validate(channelID) != nil {
		return sendBudget{}, errors.New("stored recipient OTP budget is invalid")
	}
	return budget, nil
}

func (s Service) storeBudget(budget sendBudget) error {
	if err := budget.validate(budget.ChannelID); err != nil {
		return err
	}
	dir := filepath.Dir(s.budgetPath(budget.ChannelID))
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	raw, err := json.Marshal(budget)
	if err != nil {
		return err
	}
	if len(raw)+1 > maxBudgetBytes {
		return errors.New("recipient OTP budget exceeds storage limit")
	}
	temporary, err := os.CreateTemp(dir, ".otp-*.tmp")
	if err != nil {
		return err
	}
	name := temporary.Name()
	defer os.Remove(name)
	if err = temporary.Chmod(0o600); err == nil {
		_, err = temporary.Write(append(raw, '\n'))
	}
	if err == nil {
		err = temporary.Sync()
	}
	if closeErr := temporary.Close(); err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err := os.Rename(name, s.budgetPath(budget.ChannelID)); err != nil {
		return err
	}
	return durable.SyncDirectory(dir)
}

func (b sendBudget) validate(channelID string) error {
	id, err := uuid.Parse(channelID)
	if err != nil || id.String() != channelID || b.Schema != budgetSchema || b.ChannelID != channelID || len(b.Sends) > maxBudgetEntries {
		return errors.New("invalid recipient OTP budget envelope")
	}
	for _, entry := range b.Sends {
		digest, err := hex.DecodeString(entry.InvitationHash)
		if err != nil || len(digest) != 32 || hex.EncodeToString(digest) != entry.InvitationHash || entry.At.IsZero() || entry.At.Year() < 1 || entry.At.Year() > 9999 {
			return errors.New("invalid recipient OTP budget entry")
		}
	}
	return nil
}

func (b *sendBudget) prune(now time.Time) {
	kept := b.Sends[:0]
	for _, send := range b.Sends {
		// Keep future entries: a backward clock step must not reset quotas.
		if now.Sub(send.At) < budgetWindow {
			kept = append(kept, send)
		}
	}
	b.Sends = kept
}

type budgetDecision int

const (
	budgetSend budgetDecision = iota
	budgetCooling
	budgetExhausted
)

// decide reports whether one more code may be mailed to this invitation now.
func (b sendBudget) decide(digest string, now time.Time, perInvitation, perChannel int, first time.Duration) budgetDecision {
	if len(b.Sends) >= perChannel {
		return budgetExhausted
	}
	count := 0
	var last time.Time
	for _, send := range b.Sends {
		if send.InvitationHash != digest {
			continue
		}
		count++
		if send.At.After(last) {
			last = send.At
		}
	}
	if count >= perInvitation {
		return budgetExhausted
	}
	if count == 0 {
		return budgetSend
	}
	pause := first
	if count >= 2 {
		pause = max(first, laterCooldowns[min(count-2, len(laterCooldowns)-1)])
	}
	if now.Sub(last) < pause {
		return budgetCooling
	}
	return budgetSend
}

func (s Service) sendsPerInvitation() int {
	if s.SendsPerInvitation > 0 {
		return s.SendsPerInvitation
	}
	return DefaultSendsPerInvitation
}

func (s Service) sendsPerChannel() int {
	if s.SendsPerChannel > 0 {
		return s.SendsPerChannel
	}
	return DefaultSendsPerChannel
}

// Keep charges for 24 hours even after recipient removal or revocation.
// Otherwise changing the recipient list could reset the channel budget.
// Expiry of an OTP epoch never clears a charge.
func (s Service) sweepBudget(root *os.Root, id string, now time.Time) error {
	budget, err := readBudget(root, id)
	if err != nil {
		return err
	}
	previous := len(budget.Sends)
	budget.prune(now)
	if len(budget.Sends) == 0 {
		err := root.Remove(budgetName)
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if len(budget.Sends) == previous {
		return nil
	}
	return s.storeBudget(budget)
}
