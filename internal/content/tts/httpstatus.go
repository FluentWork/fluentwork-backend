package tts

import (
	"errors"
	"fmt"
)

type httpStatusError struct {
	Status int
	LogID  string
}

func (e *httpStatusError) Error() string {
	if e == nil {
		return "tts: unexpected http status"
	}
	return fmt.Sprintf("tts: unexpected http status: http %d log_id=%s", e.Status, e.LogID)
}

func (e *httpStatusError) Unwrap() error {
	return ErrHTTPStatus
}

// IsHTTP5xx reports whether err is an upstream HTTP 5xx from Volc streaming TTS.
func IsHTTP5xx(err error) bool {
	var he *httpStatusError
	return errors.As(err, &he) && he != nil && he.Status >= 500 && he.Status <= 599
}
