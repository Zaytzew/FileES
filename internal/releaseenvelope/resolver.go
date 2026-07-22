package releaseenvelope

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"
)

type Fetcher interface {
	Cat(context.Context, string) ([]byte, error)
}

type SignatureVerifier interface {
	Verify(context.Context, string, []byte, []byte) error
}

type Resolver struct {
	Fetcher     Fetcher
	Verifier    SignatureVerifier
	TrustedKeys []string
	Now         func() time.Time
}

type Resolved struct {
	Envelope     *Envelope
	Component    Component
	Manifest     *ArtifactManifest
	SigningKeyID string
}

func (resolver Resolver) Resolve(ctx context.Context, channelPath, componentName, platform string) (*Resolved, error) {
	if resolver.Fetcher == nil || resolver.Verifier == nil || len(resolver.TrustedKeys) == 0 {
		return nil, errors.New("release resolver requires fetcher, verifier and trusted keyring")
	}
	channelPath, err := safeRelative(channelPath)
	if err != nil {
		return nil, fmt.Errorf("channel path: %w", err)
	}
	envelopeData, err := resolver.Fetcher.Cat(ctx, channelPath)
	if err != nil {
		return nil, fmt.Errorf("fetch release envelope: %w", err)
	}
	envelopeSignature, err := resolver.Fetcher.Cat(ctx, channelPath+".sig")
	if err != nil {
		return nil, fmt.Errorf("fetch release envelope signature: %w", err)
	}
	verifiedKey, err := resolver.verifyAny(ctx, envelopeData, envelopeSignature)
	if err != nil {
		return nil, fmt.Errorf("verify release envelope: %w", err)
	}
	now := time.Now().UTC()
	if resolver.Now != nil {
		now = resolver.Now().UTC()
	}
	envelope, err := ParseEnvelope(envelopeData, now)
	if err != nil {
		return nil, fmt.Errorf("parse release envelope: %w", err)
	}
	if envelope.KeyID != verifiedKey {
		return nil, fmt.Errorf("release envelope key_id %q does not match signing key %q", envelope.KeyID, verifiedKey)
	}
	component, err := envelope.Select(strings.TrimSpace(componentName), strings.TrimSpace(platform))
	if err != nil {
		return nil, err
	}
	manifestData, err := resolver.Fetcher.Cat(ctx, component.Manifest)
	if err != nil {
		return nil, fmt.Errorf("fetch artifact manifest: %w", err)
	}
	manifestSignature, err := resolver.Fetcher.Cat(ctx, component.Manifest+".sig")
	if err != nil {
		return nil, fmt.Errorf("fetch artifact manifest signature: %w", err)
	}
	if err := resolver.Verifier.Verify(ctx, verifiedKey, manifestData, manifestSignature); err != nil {
		return nil, fmt.Errorf("verify artifact manifest: %w", err)
	}
	manifest, err := ParseArtifactManifest(manifestData)
	if err != nil {
		return nil, fmt.Errorf("parse artifact manifest: %w", err)
	}
	if err := envelope.ValidateManifest(component, manifest); err != nil {
		return nil, err
	}
	return &Resolved{Envelope: envelope, Component: component, Manifest: manifest, SigningKeyID: verifiedKey}, nil
}

func (resolver Resolver) verifyAny(ctx context.Context, message, signature []byte) (string, error) {
	seen := make(map[string]struct{}, len(resolver.TrustedKeys))
	for _, keyID := range resolver.TrustedKeys {
		keyID = strings.TrimSpace(keyID)
		if !identifierPattern.MatchString(keyID) {
			return "", fmt.Errorf("trusted key ID %q is invalid", keyID)
		}
		if _, duplicate := seen[keyID]; duplicate {
			continue
		}
		seen[keyID] = struct{}{}
		if err := resolver.Verifier.Verify(ctx, keyID, message, signature); err == nil {
			return keyID, nil
		}
	}
	return "", errors.New("signature does not match any trusted release key")
}
