package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
)

// Native presentation reads the same data-only catalogue as the WebView.
// No JavaScript is evaluated, and no renderer-provided path is opened.
type nativeLanguage struct {
	catalogues map[string]map[string]json.RawMessage
	locale     string
}

// The optional default preserves Polish for callers outside the live host.
// Live presentation always passes its explicitly resolved language.
func nativePresentationLanguage(locales []nativeLanguage) nativeLanguage {
	if len(locales) > 0 {
		return locales[0]
	}
	language, _ := loadNativeLanguage()
	language.selectLocale("pl")
	return language
}

var nativeTextArgument = regexp.MustCompile(`\{([a-zA-Z][\w]*)\}`)

// Store an immutable language snapshot; action goroutines never share the
// tray's mutable locale or hold its UI mutex while opening a dialog.
func (service *GUIService) setPresentationLanguage(language nativeLanguage) {
	service.presentationLanguage.Store(&language)
}

func (service *GUIService) localizeText(key, fallback string) string {
	if language := service.presentationLanguage.Load(); language != nil {
		return language.text(key)
	}
	return fallback
}

func (language nativeLanguage) format(key string, args map[string]string) string {
	return nativeTextArgument.ReplaceAllStringFunc(language.text(key), func(token string) string {
		if value, ok := args[token[1:len(token)-1]]; ok {
			return value
		}
		return token
	})
}

func loadNativeLanguage() (nativeLanguage, error) {
	result := nativeLanguage{catalogues: make(map[string]map[string]json.RawMessage), locale: "en"}
	entries, err := frontend.ReadDir("frontend/locales")
	if err != nil {
		return result, err
	}
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".js") {
			continue
		}
		data, err := frontend.ReadFile("frontend/locales/" + entry.Name())
		if err != nil {
			return result, err
		}
		_, body, ok := strings.Cut(string(data), "export default ")
		if !ok {
			return result, fmt.Errorf("invalid GUI catalogue %s", entry.Name())
		}
		var messages map[string]json.RawMessage
		if err := json.Unmarshal([]byte(strings.TrimSuffix(strings.TrimSpace(body), ";")), &messages); err != nil {
			return result, err
		}
		result.catalogues[strings.TrimSuffix(entry.Name(), ".js")] = messages
	}
	if result.catalogues["en"] == nil {
		return result, fmt.Errorf("missing English GUI catalogue")
	}
	return result, nil
}

// Called under the tray mutex. The main WebView resolves System using its
// navigator language; host preference is transient, not daemon state.
func (language *nativeLanguage) selectLocale(locale string) bool {
	if language.catalogues[locale] == nil || language.locale == locale {
		return false
	}
	language.locale = locale
	return true
}

func (language nativeLanguage) text(key string) string {
	for _, locale := range []string{language.locale, "en"} {
		var value string
		if data, ok := language.catalogues[locale][key]; ok && json.Unmarshal(data, &value) == nil {
			return value
		}
	}
	return "[" + key + "]"
}
