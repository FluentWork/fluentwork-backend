// Package apierr defines the unified API error envelope {code, message, request_id}.
package apierr

import "fmt"

// Error is a client-facing API error with a stable machine-readable code.
type Error struct {
	Code       string
	Message    string
	HTTPStatus int
}

// Error implements the error interface.
func (e *Error) Error() string {
	if e == nil {
		return ""
	}
	return fmt.Sprintf("%s: %s", e.Code, e.Message)
}

// Body is the JSON error envelope from the backend technical design.
type Body struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	RequestID string `json:"request_id"`
}

// InvalidArgument returns a 400 error.
func InvalidArgument(message string) *Error {
	return &Error{Code: "INVALID_ARGUMENT", Message: message, HTTPStatus: 400}
}

// Unauthenticated returns a 401 error.
func Unauthenticated(message string) *Error {
	return &Error{Code: "UNAUTHENTICATED", Message: message, HTTPStatus: 401}
}

// PermissionDenied returns a 403 error.
func PermissionDenied(message string) *Error {
	return &Error{Code: "PERMISSION_DENIED", Message: message, HTTPStatus: 403}
}

// NotFound returns a 404 error.
func NotFound(message string) *Error {
	return &Error{Code: "NOT_FOUND", Message: message, HTTPStatus: 404}
}

// Conflict returns a 409 error.
func Conflict(message string) *Error {
	return &Error{Code: "CONFLICT", Message: message, HTTPStatus: 409}
}

// Unavailable returns a 503 error.
func Unavailable(message string) *Error {
	return &Error{Code: "UNAVAILABLE", Message: message, HTTPStatus: 503}
}

// Internal returns a 500 error that does not leak internals.
func Internal(message string) *Error {
	return &Error{Code: "INTERNAL", Message: message, HTTPStatus: 500}
}
