package voicegateway

// UplinkChunkBytes is the size of audio chunks sent from client to vendor:
// 20ms of 16 kHz mono s16le = 640 bytes. It is what
// ClientAudioFormat.FrameBytes(20) returns.
//
// provider_dev_echo.go uses this constant. voiceduplex/volc_duplex.go needs the
// same size but cannot import this package — it is the layer below, and the
// import would cycle — so it declares its own uplinkChunkBytes with the same
// value. The two are the same number and nothing ties them together: changing
// this constant will not make that one fail to build, or fail a test.
//
// It is declared in voicegateway rather than voiceduplex because it is
// conceptually part of the client audio format specification.
const UplinkChunkBytes = 640
