package deploy

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
)

func TestOpenBSDCompiledBootstrapIntegration(t *testing.T) {
	address := os.Getenv("FILEES_S2_LAB_ADDRESS")
	knownHosts := os.Getenv("FILEES_S2_LAB_KNOWN_HOSTS")
	if address == "" || knownHosts == "" {
		t.Skip("set FILEES_S2_LAB_ADDRESS and FILEES_S2_LAB_KNOWN_HOSTS")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	requestID := uuid.NewString()
	response, err := submitOnboarding(ctx, address, knownHosts, "s2-lab@example.net", requestID)
	if err != nil {
		t.Fatal(err)
	}
	if response.OnboardingRequestID != requestID || response.Status != "accepted" {
		t.Fatalf("unexpected response: %+v", response)
	}
}
