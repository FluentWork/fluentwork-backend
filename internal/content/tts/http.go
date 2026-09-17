package tts

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"log/slog"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

const internalTokenHeader = "X-Internal-Token"

// UsageRecorder is the accounting seam (P1-5). It takes plain numbers so this
// package does not depend on the cost ledger's types.
type UsageRecorder interface {
	// RecordTTSUsage records one synthesis. chars is the vendor's billing unit.
	RecordTTSUsage(ctx context.Context, voiceID string, chars int) error
}

// ttsUsageTimeout bounds the accounting write, which happens after synthesis
// succeeded and must not hold the response open.
const ttsUsageTimeout = 2 * time.Second

// Handler exposes internal TTS HTTP endpoints.
type Handler struct {
	provider Provider
	recorder UsageRecorder
	logger   *slog.Logger
}

// NewHandler constructs a TTS HTTP handler. provider may be nil; synthesize
// then returns UNAVAILABLE until app-server wires a live Manager.
func NewHandler(provider Provider) *Handler {
	return &Handler{provider: provider, logger: slog.Default()}
}

// SetUsageRecorder attaches the cost ledger. Nil is a supported configuration —
// the endpoint then synthesizes without accounting, which is the state P1-5
// closed and which is still better than refusing to speak.
func (h *Handler) SetUsageRecorder(recorder UsageRecorder) {
	if h == nil {
		return
	}
	h.recorder = recorder
}

// RegisterInternalRoutes mounts POST /internal/v1/tts/synthesize.
func RegisterInternalRoutes(rg gin.IRouter, h *Handler, expectedToken string) {
	if h == nil {
		return
	}
	rg.POST("/tts/synthesize", requireInternalToken(expectedToken), h.PostSynthesize)
}

func requireInternalToken(expected string) gin.HandlerFunc {
	expected = strings.TrimSpace(expected)
	return func(c *gin.Context) {
		if expected == "" {
			httpjson.Error(c, apierr.Internal("internal API token is not configured"))
			return
		}
		got := strings.TrimSpace(c.GetHeader(internalTokenHeader))
		if got == "" || len(got) != len(expected) || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			httpjson.Error(c, apierr.Unauthenticated("invalid internal token"))
			return
		}
		c.Next()
	}
}

// SynthesizeRequest is the body of POST /internal/v1/tts/synthesize.
type SynthesizeRequest struct {
	Text    string `json:"text"`
	VoiceID string `json:"voice_id"`
}

// SynthesizeResponse is returned after audio chunks are collected.
type SynthesizeResponse struct {
	VoiceID      string `json:"voice_id"`
	Chunks       int    `json:"chunks"`
	AudioBase64  string `json:"audio_base64"`
	UsedFallback bool   `json:"used_fallback"`
}

// PostSynthesize handles POST /internal/v1/tts/synthesize.
func (h *Handler) PostSynthesize(c *gin.Context) {
	// Redundant with Collect's own nil check, and deliberate: reporting the
	// unconfigured provider before the body is read keeps a switched-off endpoint
	// from answering "your JSON is bad". The environment problem outranks the
	// caller's. Pinned by TestPostSynthesize_UnconfiguredOutranksMalformedBody.
	if h == nil || h.provider == nil {
		httpjson.Error(c, apierr.Unavailable("tts provider is not configured"))
		return
	}
	var req SynthesizeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	if q := strings.TrimSpace(c.Query("voice_id")); q != "" {
		req.VoiceID = q
	}
	voice := LookupVoice(req.VoiceID)
	chunks, err := Collect(c.Request.Context(), h.provider, req.Text, voice)
	if err != nil {
		httpjson.Error(c, mapTTSError(err))
		return
	}
	var payload []byte
	for _, chunk := range chunks {
		payload = append(payload, chunk.Data...)
	}
	usedFallback := false
	if reporter, ok := h.provider.(interface{ UsingFallback() bool }); ok {
		usedFallback = reporter.UsingFallback()
	}
	h.recordUsage(c.Request.Context(), req.Text, voice.VoiceID)
	httpjson.OK(c, SynthesizeResponse{
		VoiceID:      voice.VoiceID,
		Chunks:       len(chunks),
		AudioBase64:  base64.StdEncoding.EncodeToString(payload),
		UsedFallback: usedFallback,
	})
}

// recordUsage files the synthesis in the cost ledger (P1-5: voice.tts).
//
// Two deliberate choices:
//
//   - the write runs on a context detached from the request, because the vendor
//     was paid whether or not the caller is still listening — a dropped
//     connection must not lose the usage row;
//   - a failure is logged, never returned: usage we failed to file is a
//     bookkeeping problem, not a reason to fail the caller.
//
// chars counts runes, which is the closest local proxy for the vendor's billing
// unit; the exact vendor count is P2-2's billing question, and this is the
// usage-is-fact half of that pair.
func (h *Handler) recordUsage(ctx context.Context, text, voiceID string) {
	if h.recorder == nil {
		return
	}
	usageCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), ttsUsageTimeout)
	defer cancel()
	if err := h.recorder.RecordTTSUsage(usageCtx, voiceID, utf8.RuneCountInString(text)); err != nil {
		h.logger.Warn("tts usage not recorded",
			"component", "tts.handler",
			"voice_id", voiceID,
			"err", err,
		)
	}
}

func mapTTSError(err error) error {
	switch {
	case errors.Is(err, ErrEmptyText):
		return apierr.InvalidArgument("text is required")
	case errors.Is(err, ErrMissingAPIKey), errors.Is(err, ErrClosed):
		return apierr.Unavailable("tts provider is not configured")
	case IsHTTP5xx(err):
		return apierr.Unavailable("tts upstream unavailable")
	default:
		return apierr.Internal("tts synthesis failed")
	}
}
