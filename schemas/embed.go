// Package schemas embeds backend-local mirrors of shared cross-repository schema assets.
package schemas

import _ "embed"

// WSSControlFramesV1 holds the backend mirror of the shared WSS control-frame schema.
//
//go:embed transport/wss-control-frames-v1.json
var WSSControlFramesV1 []byte

// SpeechObservabilityEventsV1 holds the backend mirror of the shared speech event schema.
//
//go:embed events/speech-observability-events-v1.json
var SpeechObservabilityEventsV1 []byte
