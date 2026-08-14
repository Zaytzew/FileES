package errcat

import (
	"fmt"
	"maps"
)

// Fault is the in-process error value. Protocols convert it to their envelope;
// they do not invent a second sentence.
type Fault struct {
	Spec
	Details map[string]string
	Cause   error
}

func (f Fault) Error() string {
	if f.Key == "" && f.Code == "" {
		if f.Cause != nil {
			return f.Cause.Error()
		}
		return "filees fault"
	}
	if f.Cause != nil {
		return fmt.Sprintf("[%s] %s: %v", f.Code, f.Key, f.Cause)
	}
	return fmt.Sprintf("[%s] %s", f.Code, f.Key)
}

func (f Fault) Unwrap() error { return f.Cause }

// PresentationError matches the GUI/ipcclient surface so a Fault can
// cross that boundary without the GUI importing a wire package.
func (f Fault) PresentationError() (code, severity, hint, message string) {
	return string(f.Code), string(f.Severity), string(f.Hint), string(f.Key)
}

// PresentationDetails returns a copy of the structured fields for this key.
func (f Fault) PresentationDetails() map[string]string {
	if len(f.Details) == 0 {
		return nil
	}
	return maps.Clone(f.Details)
}

// New builds a Fault from a registered key. Unknown keys still produce a
// Fault so a missing dictionary entry cannot become a silent nil.
func New(key Key, details map[string]string, cause error) Fault {
	spec, ok := ByKey(key)
	if !ok {
		spec = Spec{Code: CodeUnknown, Key: key, Severity: SevError, Hint: HintRetryLocal, Diagnostic: "Unexpected error", Polish: "Nieoczekiwany błąd"}
	}
	return Fault{Spec: spec, Details: cloneDetails(details), Cause: cause}
}

// Of returns a Fault for an exact (code, key) pair when the catalog has it,
// otherwise the key's preferred spec.
func Of(code Code, key Key, details map[string]string, cause error) Fault {
	if spec, ok := ByPair(code, key); ok {
		return Fault{Spec: spec, Details: cloneDetails(details), Cause: cause}
	}
	return New(key, details, cause)
}

func cloneDetails(in map[string]string) map[string]string {
	if len(in) == 0 {
		return nil
	}
	return maps.Clone(in)
}
