package errcat

const (
	CodePassportDenied          Code = "LOCK-2101"
	CodePassportStale           Code = "LOCK-2102"
	CodePassportUncertain       Code = "LOCK-2103"
	CodePassportUnavailable     Code = "LOCK-2104"
	CodePassportRequestConflict Code = "LOCK-2105"
	CodePathOwnerUnavailable    Code = "LOCK-2106"
	KeyPassportDenied           Key  = "passport.replacement_denied"
	KeyPassportStale            Key  = "passport.replacement_stale"
	KeyPassportUncertain        Key  = "passport.replacement_uncertain"
	KeyPassportUnavailable      Key  = "passport.replacement_unavailable"
	KeyPassportRequestConflict  Key  = "passport.replacement_request_conflict"
	KeyPathOwnerUnavailable     Key  = "passport.path_owner_unavailable"
)
