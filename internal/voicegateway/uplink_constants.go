package voicegateway

// UplinkChunkBytes is the size of audio chunks sent from client to vendor:
// 20ms of 16 kHz mono s16le = 640 bytes.
//
// The value is derived from ClientAudioFormat.FrameBytes(20), and both uplink
// paths (voiceduplex/volc_duplex.go and provider_dev_echo.go) use this size.
//
// This constant is declared in voicegateway because it is conceptually part of
// the client audio format specification. However, voiceduplex (a lower layer)
// also needs it, so we accept the layering compromise to avoid an import cycle.
const UplinkChunkBytes = 640
