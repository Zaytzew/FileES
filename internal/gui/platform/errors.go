package platform

import (
	"errors"
	"fmt"
)

// FailureKind classifies failures that require different UX. Operational
// failures retain their original cause and use FailureOperational.
type FailureKind string

const (
	FailureUnavailable FailureKind = "unavailable"
	FailureOperational FailureKind = "operational"
)

// Failure is returned by native adapters when an operation cannot be provided
// or fails. Context cancellation remains context.Canceled/DeadlineExceeded.
type Failure struct {
	Kind    FailureKind
	Feature string
	Err     error
}

func (e *Failure) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return fmt.Sprintf("platform %s: %s", e.Feature, e.Kind)
	}
	return fmt.Sprintf("platform %s: %s: %v", e.Feature, e.Kind, e.Err)
}

func (e *Failure) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

func NewUnavailable(feature string, cause error) error {
	return &Failure{Kind: FailureUnavailable, Feature: feature, Err: cause}
}

func NewOperationalFailure(feature string, cause error) error {
	return &Failure{Kind: FailureOperational, Feature: feature, Err: cause}
}

func IsFailure(err error, kind FailureKind) bool {
	var failure *Failure
	return errors.As(err, &failure) && failure.Kind == kind
}
