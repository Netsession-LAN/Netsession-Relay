// Package uimsg marks errors whose text is shown to the user as it is.
package uimsg

// Error is an error whose text is a complete sentence for the web UI (German source text, translated when it is
// shown). Go's convention of short lower-case error strings is for errors that are wrapped and logged; these are
// messages, so they start with a capital letter and end with a full stop.
//
// An Error is display-only. It is created by the web part at the place where an answer is worded and must never
// be compared or matched (two values with the same text are equal, so its identity would be its wording).
// Packages below the web part return ordinary errors with an identity (errors.Is) instead; the web part maps
// those to an Error.
type Error string

func (e Error) Error() string { return string(e) }
