package reviewgen

import (
	"errors"
	"fmt"
	"strings"
)

// FailureKind classifies why one generation attempt failed (P0-2). The session
// layer records it as a log field so that "empty session, correctly rejected"
// never reads like "the model returned truncated JSON" in production.
type FailureKind string

const (
	// FailureInvalidRequest is a caller bug: a required input is missing.
	FailureInvalidRequest FailureKind = "invalid_request"
	// FailureEmptySession is a session with no user speech. Rejecting it is
	// correct behaviour, not an incident (docs/77 P0-15 A).
	FailureEmptySession FailureKind = "empty_session"
	// FailureTransport covers connection, timeout and non-200 responses.
	FailureTransport FailureKind = "transport"
	// FailureEmptyContent is a response that carried no message content.
	FailureEmptyContent FailureKind = "empty_content"
	// FailureTruncatedJSON is model JSON cut off mid-document — almost always
	// finish_reason=length (docs/77 P0-15 B).
	FailureTruncatedJSON FailureKind = "truncated_json"
	// FailureInvalidJSON is model JSON that is complete but unparseable.
	FailureInvalidJSON FailureKind = "invalid_json"
	// FailureMissingFields is parseable JSON without the review/refine keys.
	FailureMissingFields FailureKind = "missing_fields"
	// FailureSchemaViolation is a document that parses but fails B15 validation.
	FailureSchemaViolation FailureKind = "schema_violation"
)

// GenerateError carries the classification plus the model's raw output, so a
// caller can log the full response (P0-2 acceptance 1) and replay the failure
// against the validator offline (acceptance 3).
type GenerateError struct {
	Kind         FailureKind
	Err          error
	SessionID    string
	FinishReason string
	// RawContent is the model's message content exactly as returned. Empty when
	// the call never produced one (transport failure).
	RawContent string
	// RawBody is the HTTP response body for failures before content extraction
	// (non-200 status, undecodable envelope).
	RawBody string
	// ValidationRules lists every B15 rule that failed, not just the first.
	ValidationRules []string
}

// Error implements error.
func (e *GenerateError) Error() string {
	if e == nil {
		return "<nil>"
	}
	if e.Err == nil {
		return string(e.Kind)
	}
	return fmt.Sprintf("%s: %v", e.Kind, e.Err)
}

// Unwrap exposes the underlying error for errors.Is / errors.As.
func (e *GenerateError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// Raw returns the model's raw response for logging: message content when there
// is one, otherwise the HTTP body.
func (e *GenerateError) Raw() string {
	if e == nil {
		return ""
	}
	if e.RawContent != "" {
		return e.RawContent
	}
	return e.RawBody
}

// LogAttrs returns the failure's diagnostic fields as slog-style key/value
// pairs: the raw response (P0-2 acceptance 1), the provider's stop reason and
// every failed B15 rule. Callers append these to their own log arguments.
func (e *GenerateError) LogAttrs() []any {
	if e == nil {
		return nil
	}
	attrs := make([]any, 0, 6)
	if raw := e.Raw(); raw != "" {
		attrs = append(attrs, "raw_response", raw)
	}
	if e.FinishReason != "" {
		attrs = append(attrs, "finish_reason", e.FinishReason)
	}
	if len(e.ValidationRules) > 0 {
		attrs = append(attrs, "validation_rules", strings.Join(e.ValidationRules, ","))
	}
	return attrs
}

// KindOf reports the classification of err, or "" when err is not a
// *GenerateError.
func KindOf(err error) FailureKind {
	var genErr *GenerateError
	if errors.As(err, &genErr) && genErr != nil {
		return genErr.Kind
	}
	return ""
}
