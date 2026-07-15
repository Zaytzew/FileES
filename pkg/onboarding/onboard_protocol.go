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
	OnboardRequestSchema  = "filees.onboard-request/v1"
	OnboardResponseSchema = "filees.onboard-response/v1"
)

type OnboardRequest struct {
	Schema              string `json:"schema"`
	Email               string `json:"email"`
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
	if request.Schema != OnboardRequestSchema {
		return OnboardRequest{}, fmt.Errorf("unsupported onboard request schema %q", request.Schema)
	}
	if _, err := canonicalEmail(request.Email); err != nil {
		return OnboardRequest{}, err
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
}

func EncodeOnboardResponse(requestID string) []byte {
	response := OnboardResponse{Schema: OnboardResponseSchema, Status: "accepted", OnboardingRequestID: requestID}
	raw, _ := json.Marshal(response)
	return append(bytes.TrimSpace(raw), '\n')
}
