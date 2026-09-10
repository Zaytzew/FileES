package ipcserver

import (
	"filees/internal/domaincatalog"
	contract "filees/pkg/contract/v1"
	"filees/pkg/errcat"
)

// handleMessagesCatalog answers with one atomic snapshot of the domain
// message catalogue.
//
// The requested locale is a parameter of the read and nothing else: this
// handler stores no preference, emits no event and changes no state, so two
// renderers can hold two languages at once and a language change in one of
// them is invisible to the other. That is why there is no messages.locale_set
// to go with this — the preference belongs to the renderer, beside its theme.
//
// Both message sets come from one read of one registry. Serving them from two
// calls would let a client that is half-way through a language change render
// a screen made of two catalogues.
func (s *Server) handleMessagesCatalog(req contract.Request) contract.Response {
	var payload contract.MessagesCatalogPayload
	if err := contract.DecodePayload(req.Payload, &payload); err != nil {
		return protoErr(req.RequestID, "proto.invalid_payload", nil)
	}
	registry, err := domaincatalog.Shared()
	if err != nil {
		// The packs are compiled in and validated by the build, so reaching
		// here means the binary is broken rather than the request.
		return contract.ErrResponseFrom(req.RequestID,
			errcat.New("messages.catalog_unavailable", nil, err))
	}

	// An unsupported tag is answered in the fallback rather than refused. A
	// reader whose language nobody has written yet still needs the sentence,
	// and Locale below tells the client what it actually got.
	locale := payload.Locale
	if _, ok := registry.Pack(locale); !ok {
		locale = domaincatalog.BaseLocale
	}
	served, _ := registry.Pack(locale)
	fallback, _ := registry.Pack(domaincatalog.BaseLocale)

	languages := make([]contract.CatalogLanguage, 0, len(registry.Locales()))
	for _, language := range registry.Locales() {
		languages = append(languages, contract.CatalogLanguage{Code: language.Code, Name: language.Name})
	}

	return contract.OKResponse(req.RequestID, contract.MessagesCatalogResult{
		Schema:            domaincatalog.Schema,
		CatalogID:         registry.Digest(),
		Locale:            locale,
		FallbackLocale:    domaincatalog.BaseLocale,
		Languages:         languages,
		DictionaryVersion: served.DictionaryVersion,
		Messages:          wireMessages(served),
		FallbackMessages:  wireMessages(fallback),
		Params:            wireParams(),
	})
}

func wireMessages(pack domaincatalog.Pack) map[string]contract.CatalogMessage {
	out := make(map[string]contract.CatalogMessage, len(pack.Messages))
	for key, message := range pack.Messages {
		out[key] = contract.CatalogMessage{
			Text:     message.Text,
			Plural:   message.Plural,
			Variants: message.Variants,
		}
	}
	return out
}

// wireParams sends the message schema alongside the templates. Without it a
// client cannot tell a timestamp it must format from a name it must leave
// alone, and cannot honour the rule that only declared fields are read.
func wireParams() map[string][]contract.CatalogParam {
	schemas := domaincatalog.Schemas()
	out := make(map[string][]contract.CatalogParam, len(schemas))
	for key, schema := range schemas {
		if len(schema.Params) == 0 {
			continue
		}
		params := make([]contract.CatalogParam, 0, len(schema.Params))
		for _, field := range schema.Params {
			params = append(params, contract.CatalogParam{Name: field.Name, Kind: string(field.Kind)})
		}
		out[key] = params
	}
	return out
}
