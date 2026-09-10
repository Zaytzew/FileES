package contracttests

import (
	"context"
	"encoding/json"
	"testing"

	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
	"filees/pkg/ipcclient"
	"filees/pkg/ipcserver"
)

func catalogFor(t *testing.T, cli *ipcclient.Client, requestID, locale string) contract.MessagesCatalogResult {
	t.Helper()
	payload, err := json.Marshal(contract.MessagesCatalogPayload{Locale: locale})
	if err != nil {
		t.Fatal(err)
	}
	resp, err := cli.Do(context.Background(), contract.Request{
		Protocol:  contract.Protocol,
		RequestID: requestID,
		ClientID:  "catalog-test",
		Command:   contract.CmdMessagesCatalog,
		Payload:   payload,
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != contract.StatusOK {
		t.Fatalf("locale %q: response = %#v", locale, resp)
	}
	var result contract.MessagesCatalogResult
	if err := json.Unmarshal(resp.Result, &result); err != nil {
		t.Fatal(err)
	}
	return result
}

func catalogServer(t *testing.T) *ipcclient.Client {
	t.Helper()
	sock := testSocketPath(t)
	server := ipcserver.New(sock)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	return ipcclient.New(sock, "catalog-test")
}

func TestMessagesCatalogServesRequestedLocaleAndFallbackTogether(t *testing.T) {
	cli := catalogServer(t)
	result := catalogFor(t, cli, "catalog-pl", "pl")

	if result.Schema != "filees.domain-catalog/v1" {
		t.Errorf("schema = %q", result.Schema)
	}
	if result.Locale != "pl" || result.FallbackLocale != "en" {
		t.Errorf("locale = %q, fallback = %q", result.Locale, result.FallbackLocale)
	}
	if result.CatalogID == "" {
		t.Error("catalog_id is empty; a client cannot tell two catalogues apart")
	}
	if len(result.Languages) < 2 {
		t.Errorf("languages = %#v", result.Languages)
	}
	for _, language := range result.Languages {
		if language.Code == "" || language.Name == "" {
			t.Errorf("language %#v has no code or no display name", language)
		}
	}
	// Both sets travel in one response and must cover the same keys: a
	// client half-way through a language change must never have to stitch
	// two catalogues together.
	if len(result.Messages) != len(result.FallbackMessages) {
		t.Fatalf("messages = %d, fallback = %d", len(result.Messages), len(result.FallbackMessages))
	}
	for key := range result.Messages {
		if _, ok := result.FallbackMessages[key]; !ok {
			t.Errorf("fallback is missing %q", key)
		}
	}
}

func TestMessagesCatalogCoversEveryDictionaryKey(t *testing.T) {
	cli := catalogServer(t)
	result := catalogFor(t, cli, "catalog-keys", "pl")
	for _, spec := range errcat.All() {
		message, ok := result.Messages[string(spec.Key)]
		if !ok {
			t.Errorf("catalogue does not carry %q", spec.Key)
			continue
		}
		if message.Text == "" && message.Plural == nil && message.Variants == nil {
			t.Errorf("%q arrived empty in every shape", spec.Key)
		}
	}
}

func TestMessagesCatalogSendsTheParameterSchema(t *testing.T) {
	cli := catalogServer(t)
	result := catalogFor(t, cli, "catalog-params", "pl")

	params, ok := result.Params["lock.held_by_other"]
	if !ok {
		t.Fatal("lock.held_by_other has no parameter schema")
	}
	kinds := map[string]string{}
	for _, param := range params {
		kinds[param.Name] = param.Kind
	}
	// The client formats a timestamp for its reader and leaves a path alone.
	// Without the kinds it would have to guess from the name.
	if kinds["until"] != "timestamp" || kinds["path"] != "path" || kinds["holder"] != "text" {
		t.Fatalf("kinds = %#v", kinds)
	}
	for key, declared := range result.Params {
		for _, param := range declared {
			if param.Name == "" || param.Kind == "" {
				t.Errorf("%s declares an incomplete parameter %#v", key, param)
			}
		}
	}
}

// The ladder has to survive the wire, or the daemon is back to sending one
// sentence for "somebody has it" and "Anna has it until 13:41".
func TestMessagesCatalogPreservesLadderOrder(t *testing.T) {
	cli := catalogServer(t)
	result := catalogFor(t, cli, "catalog-ladder", "pl")

	message, ok := result.Messages["lock.held_by_other"]
	if !ok || len(message.Variants) < 2 {
		t.Fatalf("message = %#v", message)
	}
	if message.Text != "" || message.Plural != nil {
		t.Errorf("a ladder must arrive as variants only: %#v", message)
	}
	first, last := message.Variants[0], message.Variants[len(message.Variants)-1]
	if first == last {
		t.Fatal("ladder collapsed to one wording")
	}
}

// An unsupported tag is answered in the fallback rather than refused, and the
// response says which language actually arrived.
func TestMessagesCatalogFallsBackForUnknownLocale(t *testing.T) {
	cli := catalogServer(t)
	result := catalogFor(t, cli, "catalog-unknown", "sv-SE")
	if result.Locale != "en" {
		t.Fatalf("locale = %q, want the fallback", result.Locale)
	}
	if len(result.Messages) == 0 {
		t.Fatal("fallback answer carries no messages")
	}
}

// Reading the catalogue is a read. Two renderers ask for two languages and
// neither sees the other's choice, because the daemon keeps no preference.
func TestMessagesCatalogIsAReadNotASetting(t *testing.T) {
	cli := catalogServer(t)
	polish := catalogFor(t, cli, "catalog-two-1", "pl")
	english := catalogFor(t, cli, "catalog-two-2", "en")
	again := catalogFor(t, cli, "catalog-two-3", "pl")

	if polish.Locale != "pl" || english.Locale != "en" || again.Locale != "pl" {
		t.Fatalf("locales = %q, %q, %q", polish.Locale, english.Locale, again.Locale)
	}
	if polish.CatalogID != english.CatalogID || polish.CatalogID != again.CatalogID {
		t.Error("the catalogue identity changed between reads of the same build")
	}
	key := "lock.invalid_path"
	if polish.Messages[key].Text == english.Messages[key].Text {
		t.Errorf("both locales returned the same sentence for %q", key)
	}
	if polish.FallbackMessages[key].Text != english.Messages[key].Text {
		t.Errorf("the fallback set is not the base locale's set")
	}
}

func TestMessagesCatalogRejectsAMalformedPayload(t *testing.T) {
	cli := catalogServer(t)
	resp, err := cli.Do(context.Background(), contract.Request{
		Protocol:  contract.Protocol,
		RequestID: "catalog-bad-payload",
		ClientID:  "catalog-test",
		Command:   contract.CmdMessagesCatalog,
		Payload:   json.RawMessage(`"pl"`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != contract.StatusError || resp.Error == nil || resp.Error.MessageKey != "proto.invalid_payload" {
		t.Fatalf("response = %#v", resp)
	}
}

// The typed client is what the GUI composition will use, so it is exercised
// against a real server rather than trusted to match the raw round trip.
func TestMessagesCatalogThroughTheTypedClient(t *testing.T) {
	sock := testSocketPath(t)
	server := ipcserver.New(sock)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	if err := server.Start(ctx); err != nil {
		t.Fatal(err)
	}
	cli := ipcclient.New(sock, "catalog-client-test")

	polish, err := cli.MessagesCatalog(context.Background(), "pl")
	if err != nil {
		t.Fatal(err)
	}
	if polish.Locale != "pl" || polish.FallbackLocale != "en" || polish.CatalogID == "" {
		t.Fatalf("result = %#v", polish)
	}
	if len(polish.Messages) == 0 || len(polish.FallbackMessages) == 0 || len(polish.Params) == 0 {
		t.Fatalf("empty snapshot: %d messages, %d fallback, %d params",
			len(polish.Messages), len(polish.FallbackMessages), len(polish.Params))
	}

	english, err := cli.MessagesCatalog(context.Background(), "en")
	if err != nil {
		t.Fatal(err)
	}
	if english.CatalogID != polish.CatalogID {
		t.Error("the catalogue identity changed between two reads of one build")
	}
	if polish.Messages["lock.invalid_path"].Text == english.Messages["lock.invalid_path"].Text {
		t.Error("both locales returned the same sentence")
	}
}
