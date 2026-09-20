package main

import "encoding/json"

// The event handles webviews whose document scripts ran before Wails' JS hook.
func systemLanguageScript(languages []string) string {
	raw, _ := json.Marshal(languages)
	return `window.__fileesSystemLanguages=` + string(raw) + `;window.dispatchEvent(new Event("filees:system-languages"));`
}
