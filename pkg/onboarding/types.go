package onboarding

import (
	"errors"
	"time"
)

const (
	TicketSchema    = "filees.onboarding-ticket/v1"
	OperationSchema = "filees.onboarding-operation/v1"
	OutboxSchema    = "filees.mail-outbox/v2"
	AuditSchema     = "filees.onboarding-audit/v1"
	BundleSchema    = "filees.onboarding-bundle/v2"
)

var (
	ErrTicketExists      = errors.New("onboarding ticket already exists")
	ErrTicketUnavailable = errors.New("onboarding ticket unavailable")
	ErrRequestConflict   = errors.New("onboarding request already exists with different parameters")
	ErrNotFound          = errors.New("onboarding record not found")
	ErrNoReversePort     = errors.New("no reverse port available")
	ErrOTPInvalid        = errors.New("invalid onboarding OTP")
	ErrOTPExpired        = errors.New("onboarding OTP expired")
	ErrTunnelGrant       = errors.New("authorized tunnel grant unavailable")
	ErrMailAttempt       = errors.New("mail outbox attempt mismatch")
)

type Policy struct {
	RealmID string `json:"realm_id"`
}

type Ticket struct {
	Schema               string    `json:"schema"`
	TicketID             string    `json:"ticket_id"`
	EmailDeliveryAddress string    `json:"email_delivery_address"`
	ApprovedPolicy       Policy    `json:"approved_policy"`
	CreatedAt            time.Time `json:"created_at"`
	ExpiresAt            time.Time `json:"expires_at"`
}

type OperationState string

const (
	OperationAwaitingTunnel   OperationState = "awaiting_tunnel"
	OperationTunnelAuthorized OperationState = "tunnel_authorized"
	OperationTunnelStarted    OperationState = "tunnel_started"
	OperationOTPExhausted     OperationState = "otp_exhausted"
	OperationExpired          OperationState = "expired"
)

// Operation deliberately has no e-mail field. The delivery address belongs
// only to the short-lived ticket/outbox path and is not client identity.
type Operation struct {
	Schema              string         `json:"schema"`
	OperationID         string         `json:"operation_id"`
	OnboardingRequestID string         `json:"onboarding_request_id"`
	OTPHash             string         `json:"otp_hash,omitempty"`
	OTPLocator          string         `json:"otp_locator,omitempty"`
	ApprovedPolicy      Policy         `json:"approved_policy"`
	AssignedReversePort uint16         `json:"assigned_reverse_port"`
	AttemptsLeft        int            `json:"attempts_left"`
	State               OperationState `json:"state"`
	CreatedAt           time.Time      `json:"created_at"`
	ExpiresAt           time.Time      `json:"expires_at"`
}

type DeliveryState string

const (
	DeliveryPending DeliveryState = "pending"
	DeliverySending DeliveryState = "sending"
	DeliveryQueued  DeliveryState = "queued"
	DeliveryFailed  DeliveryState = "failed"
)

type MailOutboxEntry struct {
	Schema          string        `json:"schema"`
	MessageID       string        `json:"message_id"`
	OperationID     string        `json:"operation_id"`
	DeliveryAddress string        `json:"delivery_address,omitempty"`
	OTP             string        `json:"otp,omitempty"`
	Template        string        `json:"template"`
	DeliveryState   DeliveryState `json:"delivery_state"`
	RetryCount      int           `json:"retry_count"`
	CreatedAt       time.Time     `json:"created_at"`
	AttemptID       string        `json:"attempt_id,omitempty"`
	AttemptedAt     *time.Time    `json:"attempted_at,omitempty"`
	LastError       string        `json:"last_error,omitempty"`
	QueuedAt        *time.Time    `json:"queued_at,omitempty"`
}

type MailJob struct {
	Entry     MailOutboxEntry `json:"entry"`
	ExpiresAt time.Time       `json:"expires_at"`
}

type AuditEvent struct {
	Schema      string    `json:"schema"`
	Event       string    `json:"event"`
	Actor       string    `json:"actor"`
	ObjectType  string    `json:"object_type"`
	ObjectID    string    `json:"object_id"`
	OperationID string    `json:"operation_id,omitempty"`
	At          time.Time `json:"at"`
}

// Bundle is the single committed filesystem record of an onboarding
// operation. Keeping operation, outbox and audit together makes one rename the
// commit boundary and leaves no database-shaped hidden state.
type Bundle struct {
	Schema    string          `json:"schema"`
	Operation Operation       `json:"operation"`
	Outbox    MailOutboxEntry `json:"outbox"`
	Audit     []AuditEvent    `json:"audit"`
}

type TakeReceipt struct {
	OperationID         string    `json:"operation_id"`
	OnboardingRequestID string    `json:"onboarding_request_id"`
	ExpiresAt           time.Time `json:"expires_at"`
}

type AuthGrant struct {
	OperationID         string
	ApprovedPolicy      Policy
	AssignedReversePort uint16
	ExpiresAt           time.Time
}
