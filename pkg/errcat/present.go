package errcat

import (
	"strings"
	"time"
)

const unknownHolder = "kogoś innego"

// Polish returns the user sentence for key. Unknown keys keep the generic
// fallback rather than echoing the key or a log sentence.
func Polish(key string) string {
	if spec, ok := ByKey(Key(key)); ok && spec.Polish != "" {
		return spec.Polish
	}
	return "Błąd zgłoszony przez daemon"
}

// PolishHint renders a hint. Informal RETRY shares the RETRY_LOCAL sentence.
func PolishHint(hint string) string {
	switch Hint(hint) {
	case HintRetry, HintRetryLocal:
		return "spróbuj ponownie"
	case HintRetryBackoff:
		return "ponowienie nastąpi później"
	case HintRequireAction:
		return "wymagane działanie użytkownika"
	case HintAdminOnly:
		return "skontaktuj się z administratorem"
	default:
		return ""
	}
}

// PolishDetailed fills the key's field template. It returns "" when the key
// has no field template or the required fields are missing, so the caller
// keeps the plain Polish() sentence instead of rendering holes.
func PolishDetailed(key string, details map[string]string) string {
	if key != string(KeyLockHeldByOther) {
		return ""
	}
	subject := "Plik"
	if path := details["path"]; path != "" {
		subject = "Plik „" + path + "”"
	}
	holder := details["holder"]
	if holder == "" {
		holder = unknownHolder
	}
	sentence := subject + " jest w tej chwili wypożyczony przez " + holder
	if until := details["until"]; until != "" {
		if parsed, err := time.Parse(time.RFC3339, until); err == nil {
			sentence += " do " + parsed.Local().Format("15:04")
		}
	}
	return sentence
}

// Diagnostic returns the English log sentence for key.
func Diagnostic(key string) string {
	if spec, ok := ByKey(Key(key)); ok && spec.Diagnostic != "" {
		return spec.Diagnostic
	}
	return "Unexpected error"
}

// KnownKey reports whether the dictionary has this message key.
func KnownKey(key string) bool {
	_, ok := ByKey(Key(strings.TrimSpace(key)))
	return ok
}
