package configtest

import "testing"

// TestConfigIsOneAServiceCanStartWith pins the promise Config's doc comment
// makes: every field Validate requires carries a value that is valid in the
// development environment, so a caller can hand the result straight to a
// service.
//
// This is a characterisation guard, not a defect criterion — it was green the
// day it was written. Its job is the day someone adds a required field to
// config.Config: without it, Config() would keep handing back a configuration
// no service accepts, and the first symptom would be one unrelated test
// failing somewhere else.
func TestConfigIsOneAServiceCanStartWith(t *testing.T) {
	if err := Config().Validate(); err != nil {
		t.Fatalf("Config() must be a configuration a service can start with, got: %v", err)
	}
}
