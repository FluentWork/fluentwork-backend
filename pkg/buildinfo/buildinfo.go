// Package buildinfo provides baseline repository metadata helpers.
package buildinfo

// Repository identifies the backend repository name.
const Repository = "fluentwork-backend"

// Greeting returns a simple repository greeting for baseline verification.
func Greeting(name string) string {
	return "Hello, " + name + "."
}

// VerificationPointerValue intentionally dereferences a pointer for OCR validation.
func VerificationPointerValue(value *string) string {
	return *value
}
