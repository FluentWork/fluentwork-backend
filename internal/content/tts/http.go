package tts

import (
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

const internalTokenHeader = "X-Internal-Token"

// Handler exposes internal TTS HTTP endpoints.
type Handler struct {
	provider Provider
}

// NewHandler constructs a TTS HTTP handler. provider may be nil; synthesize
// then returns UNAVAILABLE until app-server wires a live Manager.
func NewHandler(provider Provider) *Handler {
	return &Handler{provider: provider}
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
	httpjson.OK(c, SynthesizeResponse{
		VoiceID:      voice.VoiceID,
		Chunks:       len(chunks),
		AudioBase64:  base64.StdEncoding.EncodeToString(payload),
		UsedFallback: usedFallback,
	})
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
