package onboarding

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"

	"github.com/google/uuid"
)

const (
	LegacyOnboardRequestSchema = "filees.onboard-request/v1"
	OnboardRequestSchema       = "filees.onboard-request/v2"
	OnboardResponseSchema      = "filees.onboard-response/v3"
	// DemoOnboardRequestSchema asks a demo server for an OTP without an
	// invitation; see Files.TakeDemo.
	DemoOnboardRequestSchema = "filees.onboard-request/demo-v1"
)

type OnboardRequest struct {
	Schema              string `json:"schema"`
	Email               string `json:"email,omitempty"`
	InvitationToken     string `json:"invitation_token,omitempty"`
	ProposedRealmID     string `json:"proposed_realm_id,omitempty"`
	InstallationUID     string `json:"installation_uid,omitempty"`
	OnboardingRequestID string `json:"onboarding_request_id"`
}

func DecodeOnboardRequest(reader io.Reader) (OnboardRequest, error) {
	decoder := json.NewDecoder(io.LimitReader(reader, 16*1024))
	decoder.DisallowUnknownFields()
	var request OnboardRequest
	if err := decoder.Decode(&request); err != nil {
		return OnboardRequest{}, fmt.Errorf("decode onboard request: %w", err)
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return OnboardRequest{}, errors.New("decode onboard request: trailing JSON value")
	}
	if request.Schema != OnboardRequestSchema && request.Schema != LegacyOnboardRequestSchema && request.Schema != DemoOnboardRequestSchema {
		return OnboardRequest{}, fmt.Errorf("unsupported onboard request schema %q", request.Schema)
	}
	if request.Schema == DemoOnboardRequestSchema {
		if _, err := canonicalEmail(request.Email); err != nil || !validDemoInstallationUID(request.InstallationUID) || request.InvitationToken != "" || request.ProposedRealmID != "" {
			return OnboardRequest{}, errors.New("demo onboarding request is invalid")
		}
	} else if request.InstallationUID != "" {
		return OnboardRequest{}, errors.New("installation_uid belongs only to a demo onboarding request")
	} else if request.Schema == LegacyOnboardRequestSchema {
		if _, err := canonicalEmail(request.Email); err != nil || request.InvitationToken != "" || request.ProposedRealmID != "" {
			return OnboardRequest{}, errors.New("legacy onboarding request is invalid")
		}
	} else if !validInvitationToken(request.InvitationToken) || !validRealmID(request.ProposedRealmID) || request.Email != "" {
		return OnboardRequest{}, errors.New("invitation onboarding request is invalid")
	}
	if _, err := uuid.Parse(request.OnboardingRequestID); err != nil {
		return OnboardRequest{}, errors.New("onboarding_request_id must be a UUID")
	}
	return request, nil
}

type OnboardResponse struct {
	Schema              string `json:"schema"`
	Status              string `json:"status"`
	OnboardingRequestID string `json:"onboarding_request_id"`
	WorkerPublicKey     string `json:"worker_public_key"`
	AssignedReversePort uint16 `json:"assigned_reverse_port"`
	// RetryAfterMinutes accompanies a demo refusal status; zero with a
	// refusal means the refusal is final.
	RetryAfterMinutes int `json:"retry_after_minutes,omitempty"`
}

func EncodeOnboardResponse(requestID, workerPublicKey string, assignedReversePort uint16) []byte {
	response := OnboardResponse{Schema: OnboardResponseSchema, Status: "accepted", OnboardingRequestID: requestID, WorkerPublicKey: workerPublicKey, AssignedReversePort: assignedReversePort}
	raw, _ := json.Marshal(response)
	return append(bytes.TrimSpace(raw), '\n')
}

// EncodeDemoRefusal answers a demo request that TakeDemo refused. Its status
// is the refusal code, which a client that did not send a demo request never
// receives.
func EncodeDemoRefusal(requestID string, refusal *DemoRefusal) []byte {
	response := OnboardResponse{Schema: OnboardResponseSchema, Status: refusal.Code, OnboardingRequestID: requestID, RetryAfterMinutes: refusal.RetryAfterMinutes()}
	raw, _ := json.Marshal(response)
	return append(bytes.TrimSpace(raw), '\n')
}
