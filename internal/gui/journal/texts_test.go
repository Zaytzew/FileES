package journal

// testTexts gives the package's tests the resolvers a live renderer supplies.
// Hints come from the daemon's catalogue in production, so the fixture answers
// with the same sentences rather than inventing new ones.
func testTexts() Texts {
	return Texts{
		Chrome: func(_, fallback string) string { return fallback },
		Hint: func(hint string) string {
			switch hint {
			case "RETRY_LOCAL", "RETRY":
				return "Spróbuj ponownie"
			case "RETRY_BACKOFF":
				return "Ponowienie nastąpi później"
			case "REQUIRE_ACTION":
				return "Wymagane działanie użytkownika"
			case "ADMIN_ONLY":
				return "Skontaktuj się z administratorem"
			default:
				return ""
			}
		},
	}
}
