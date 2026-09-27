package contracttests

import (
	"encoding/json"
	v1 "filees/pkg/mobile/v1"
	"filees/pkg/mobileclient/androidbind"
	"github.com/google/uuid"
	"testing"
)

func TestMobileRecoveryErrorEnvelopeAndPresentation(t *testing.T) {
	for _, code := range []string{"storage.full", "operation.uncertain"} {
		id := uuid.NewString()
		resp, err := v1.NewError(id, v1.OpUploadTree, v1.ErrorBody{Code: code, Message: "safe diagnostic"})
		if err != nil {
			t.Fatal(err)
		}
		raw, err := json.Marshal(resp)
		if err != nil {
			t.Fatal(err)
		}
		got, err := v1.ParseResponse(raw)
		if err != nil || got.RequestID != id || got.Status != v1.StatusError || got.Error == nil || got.Error.Code != code {
			t.Fatalf("wire %s %v", raw, err)
		}
		for _, lang := range []string{"pl", "en", "de", "fr", "es"} {
			if androidbind.ExplainIn("mobile operation failed: "+code+": "+got.Error.Message, lang) == "" {
				t.Fatalf("missing %s/%s", code, lang)
			}
		}
	}
}
