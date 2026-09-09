package tts

import "context"

// Collect drains one Stream into a slice. The channel is fully consumed even if
// ctx is cancelled after Stream returns, so providers can release the session.
func Collect(ctx context.Context, provider Provider, text string, voice VoiceConfig) ([]AudioChunk, error) {
	if provider == nil {
		return nil, ErrClosed
	}
	ch, err := provider.Stream(ctx, text, voice)
	if err != nil {
		return nil, err
	}
	var chunks []AudioChunk
	for chunk := range ch {
		if ctx.Err() != nil {
			return chunks, ctx.Err()
		}
		chunks = append(chunks, chunk)
	}
	return chunks, nil
}
