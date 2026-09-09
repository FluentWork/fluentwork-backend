// Package schemas embeds backend-local mirrors of shared cross-repository schema assets.
package schemas

import _ "embed"

// WSSControlFramesV1 holds the backend mirror of the shared WSS control-frame schema.
//
//go:embed transport/wss-control-frames-v1.json
var WSSControlFramesV1 []byte

// WSSControlFramesV2 holds WSS V2 control-frame schema (ai.tts.start/end +
// optional server_ts_ms). ai.tts.audio is documented as binary, not JSON.
//
//go:embed transport/wss-control-frames-v2.json
var WSSControlFramesV2 []byte

// SpeechObservabilityEventsV1 holds the backend mirror of the shared speech event schema.
//
//go:embed events/speech-observability-events-v1.json
var SpeechObservabilityEventsV1 []byte
