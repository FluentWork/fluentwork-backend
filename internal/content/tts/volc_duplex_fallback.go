package tts

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
)

const duplexPingFailLimit = 3

// ErrPingFailed means Ping failed duplexPingFailLimit times in a row.
var ErrPingFailed = errors.New("tts: duplex ping failed 3 times")

// duplexAudioEvent is one duplex server event that may carry TTS audio.
type duplexAudioEvent struct {
	Type  string
	Delta string
}

type duplexTTSConn interface {
	RequestTTS(ctx context.Context, text string) error
	Recv(ctx context.Context) (duplexAudioEvent, error)
	Close(ctx context.Context) error
}

// VolcDuplexFallbackProvider synthesizes speech through the existing Volc
// duplex session path. Voice is fixed to DuplexConfig.Voice (default
// zh_female_vv_jupiter_bigtts). One duplex session is reused across Stream
// calls so T-TTS-5 can switch to and from this fallback without re-dialing.
type VolcDuplexFallbackProvider struct {
	Config voicepoc.DuplexConfig
	Logger *slog.Logger

	now  func() time.Time
	open func(context.Context) (duplexTTSConn, error)
	ping func(context.Context) error

	mu        sync.Mutex
	conn      duplexTTSConn
	pingFails atomic.Int32
	closed    atomic.Bool
}

var _ Provider = (*VolcDuplexFallbackProvider)(nil)

// Ping probes duplex liveness. Consecutive failures are counted; the third
// returns ErrPingFailed so T-TTS-5 can stop using this fallback.
func (p *VolcDuplexFallbackProvider) Ping(ctx context.Context) error {
	if p == nil || p.closed.Load() {
		return ErrClosed
	}
	err := p.doPing(ctx)
	if err == nil {
		p.pingFails.Store(0)
		return nil
	}
	n := p.pingFails.Add(1)
	if n >= duplexPingFailLimit {
		return fmt.Errorf("%w: %d consecutive failures: %v", ErrPingFailed, n, err)
	}
	return nil
}

// Close tears down the reused duplex session. It is idempotent.
func (p *VolcDuplexFallbackProvider) Close() error {
	if p == nil {
		return nil
	}
	p.closed.Store(true)
	p.mu.Lock()
	conn := p.conn
	p.conn = nil
	p.mu.Unlock()
	if conn == nil {
		return nil
	}
	return conn.Close(context.Background())
}

// Stream prompts the reused duplex session to speak text and yields audio
// chunks parsed from response.output_audio.* events.
func (p *VolcDuplexFallbackProvider) Stream(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error) {
	if p == nil || p.closed.Load() {
		return nil, ErrClosed
	}
	text, _, err := NormalizeStreamInput(text, voice)
	if err != nil {
		return nil, err
	}

	p.mu.Lock()
	conn, err := p.ensureConnLocked(ctx)
	if err != nil {
		p.mu.Unlock()
		return nil, err
	}
	if err := conn.RequestTTS(ctx, duplexReadPrompt(text)); err != nil {
		p.mu.Unlock()
		return nil, err
	}

	out := make(chan AudioChunk)
	go func() {
		defer close(out)
		defer p.mu.Unlock()
		readDuplexAudio(ctx, conn, out, p.nowMilli())
	}()
	return out, nil
}

func (p *VolcDuplexFallbackProvider) doPing(ctx context.Context) error {
	if p.ping != nil {
		return p.ping(ctx)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	_, err := p.ensureConnLocked(ctx)
	return err
}

func (p *VolcDuplexFallbackProvider) ensureConnLocked(ctx context.Context) (duplexTTSConn, error) {
	if p.conn != nil {
		return p.conn, nil
	}
	open := p.open
	if open == nil {
		open = p.defaultOpen
	}
	conn, err := open(ctx)
	if err != nil {
		return nil, err
	}
	p.conn = conn
	return conn, nil
}

func (p *VolcDuplexFallbackProvider) defaultOpen(ctx context.Context) (duplexTTSConn, error) {
	cfg := p.Config
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, ErrMissingAPIKey
	}
	if strings.TrimSpace(cfg.Voice) == "" {
		cfg.Voice = defaultVolcTTSSpeaker
	}
	sess, err := voicepoc.OpenDuplex(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return liveDuplexTTSConn{sess: sess}, nil
}

func (p *VolcDuplexFallbackProvider) nowMilli() int64 {
	now := time.Now
	if p != nil && p.now != nil {
		now = p.now
	}
	return now().UnixMilli()
}

type liveDuplexTTSConn struct {
	sess *voicepoc.DuplexSession
}

func (c liveDuplexTTSConn) RequestTTS(ctx context.Context, text string) error {
	return c.sess.RequestTextTTS(ctx, text)
}

func (c liveDuplexTTSConn) Recv(ctx context.Context) (duplexAudioEvent, error) {
	evt, err := c.sess.Recv(ctx)
	if err != nil {
		return duplexAudioEvent{}, err
	}
	return duplexAudioEvent{Type: evt.Type, Delta: evt.Delta}, nil
}

func (c liveDuplexTTSConn) Close(ctx context.Context) error {
	return c.sess.Close(ctx)
}

func duplexReadPrompt(text string) string {
	return "请一字不差地朗读以下内容，不要添加前后缀：\n" + text
}

func readDuplexAudio(ctx context.Context, conn duplexTTSConn, out chan<- AudioChunk, detectedAt int64) {
	seq := 0
	var pending *AudioChunk
	emitPending := func(final bool) bool {
		if pending == nil {
			return true
		}
		chunk := *pending
		chunk.IsFinal = final
		select {
		case <-ctx.Done():
			return false
		case out <- chunk:
			pending = nil
			return true
		}
	}

	for {
		if ctx.Err() != nil {
			return
		}
		evt, err := conn.Recv(ctx)
		if err != nil {
			if errors.Is(err, io.EOF) {
				_ = emitPending(true)
			}
			return
		}
		switch evt.Type {
		case "response.output_audio.delta":
			if strings.TrimSpace(evt.Delta) == "" {
				continue
			}
			data, err := decodeVolcAudio(evt.Delta)
			if err != nil {
				return
			}
			if !emitPending(false) {
				return
			}
			pending = &AudioChunk{Data: data, Seq: seq, DetectedAt: detectedAt}
			seq++
		case "response.output_audio.done", "response.done":
			_ = emitPending(true)
			return
		case "error":
			return
		default:
			// Ignore ASR/text events; this fallback only consumes TTS audio.
		}
	}
}
