package voicegateway

import (
	"bytes"
	"encoding/binary"
	"math/rand"
	"testing"
)

// batchResampleReference is the original whole-buffer implementation, kept
// here verbatim as the reference the streaming form must reproduce.
//
// It is deliberately a *second* implementation: comparing pcmResampler against
// the code it replaced is the only way to show the streaming rewrite did not
// change a single output byte. Comparing it against itself would prove nothing.
func batchResampleReference(pcm []byte) []byte {
	const inSamples, outSamples = duplexOutputRate / 8000, clientPlaybackRate / 8000 // 3:2

	if len(pcm)%2 != 0 {
		pcm = pcm[:len(pcm)-1]
	}
	inCount := len(pcm) / 2
	outCount := inCount * outSamples / inSamples
	if outCount == 0 {
		return nil
	}
	out := make([]byte, outCount*2)
	for i := 0; i < outCount; i++ {
		pos := float64(i) * float64(inSamples) / float64(outSamples)
		lo := int(pos)
		hi := lo + 1
		if hi >= inCount {
			hi = inCount - 1
		}
		a := float64(int16(binary.LittleEndian.Uint16(pcm[lo*2:])))
		b := float64(int16(binary.LittleEndian.Uint16(pcm[hi*2:])))
		binary.LittleEndian.PutUint16(out[i*2:], uint16(int16(a+(b-a)*(pos-float64(lo)))))
	}
	return out
}

func randomPCM(t *testing.T, samples int, seed int64) []byte {
	t.Helper()
	rng := rand.New(rand.NewSource(seed))
	buf := make([]byte, samples*2)
	for i := 0; i < samples; i++ {
		// Full int16 range so a sign or rounding slip cannot hide.
		binary.LittleEndian.PutUint16(buf[i*2:], uint16(int16(rng.Intn(65536)-32768)))
	}
	return buf
}

// The streaming form must not change a single output byte. This is the bridge
// that makes the per-chunk rewrite safe to ship.
func TestPCMResamplerMatchesTheBatchReference(t *testing.T) {
	t.Parallel()

	for _, samples := range []int{0, 1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 63, 100, 1000, 4096} {
		pcm := randomPCM(t, samples, int64(samples)+1)
		want := batchResampleReference(pcm)

		var r pcmResampler
		got := r.Write(pcm)
		if !bytes.Equal(got, want) {
			t.Fatalf("%d samples: streaming output differs from the batch reference (got %d bytes, want %d)",
				samples, len(got), len(want))
		}
		// Nothing held back: the batch form has no separate flush either.
		if int(r.OutSamples())*2 != len(want) {
			t.Fatalf("%d samples: produced %d samples, want %d", samples, r.OutSamples(), len(want)/2)
		}
	}
}

// A trailing odd byte is a real case (chunk sizes are not guaranteed even).
// The batch form drops it at the end of the stream; the streaming form must
// carry it to the next chunk instead of pairing it with nothing.
func TestPCMResamplerCarriesATrailingOddByte(t *testing.T) {
	t.Parallel()

	pcm := randomPCM(t, 30, 7)
	withOdd := append(append([]byte{}, pcm...), 0xAB)

	want := batchResampleReference(withOdd)
	var r pcmResampler
	got := r.Write(withOdd)
	if !bytes.Equal(got, want) {
		t.Fatalf("trailing odd byte: streaming differs from batch reference")
	}

	// And the same when the odd byte arrives as its own chunk: it must join the
	// next chunk's first byte rather than be dropped early.
	var split pcmResampler
	gotSplit := append(split.Write(pcm), split.Write([]byte{0xAB})...)
	if !bytes.Equal(gotSplit, want) {
		t.Fatalf("odd byte as its own chunk: differs from batch reference")
	}
}

// The property that makes streaming possible at all: the output must not depend
// on where the chunk boundaries fall. This is what a naive per-chunk resample
// gets wrong — it restarts the interpolation phase at every seam.
func TestPCMResamplerIsInvariantToChunking(t *testing.T) {
	t.Parallel()

	const samples = 4096
	pcm := randomPCM(t, samples, 99)

	var whole pcmResampler
	want := whole.Write(pcm)

	rng := rand.New(rand.NewSource(2026))
	for trial := 0; trial < 50; trial++ {
		var r pcmResampler
		var got []byte
		for offset := 0; offset < len(pcm); {
			n := 1 + rng.Intn(97) // ragged, odd and even, never aligned to 3:2
			if offset+n > len(pcm) {
				n = len(pcm) - offset
			}
			got = append(got, r.Write(pcm[offset:offset+n])...)
			offset += n
		}
		if !bytes.Equal(got, want) {
			t.Fatalf("trial %d: output differs from the single-chunk output — chunk boundaries leaked into the samples", trial)
		}
	}
}

// The premise the whole design rests on: output sample i depends only on input
// samples floor(1.5*i) and floor(1.5*i)+1, never on the total length. That holds
// exactly while the upstream clamp `hi >= inCount` never fires.
//
// If someone changes the rate constants so that it starts firing, the output
// would become length-dependent, pcmResampler would silently drop its last
// sample, and this test is the thing that says so.
func TestResamplerOutputDoesNotDependOnTotalLength(t *testing.T) {
	t.Parallel()

	const inSamples, outSamples = duplexOutputRate / 8000, clientPlaybackRate / 8000
	for inCount := 1; inCount <= 5000; inCount++ {
		outCount := inCount * outSamples / inSamples
		for i := 0; i < outCount; i++ {
			lo := i * inSamples / outSamples
			if hi := lo + 1; hi >= inCount {
				t.Fatalf(
					"the clamp fires at inCount=%d i=%d: output now depends on the total length, "+
						"so a streaming resampler cannot reproduce the batch output",
					inCount, i,
				)
			}
		}
	}
}

// Streaming has to return partial output as soon as it can, or the whole point
// is lost — a resampler that only emits at the end would reintroduce exactly the
// latency this change removes.
func TestPCMResamplerEmitsBeforeTheStreamEnds(t *testing.T) {
	t.Parallel()

	var r pcmResampler
	// 300 input samples is 12.5 ms of vendor audio — a fraction of one vendor
	// chunk, and far less than a turn.
	early := r.Write(randomPCM(t, 300, 3))
	if len(early) == 0 {
		t.Fatal("nothing emitted after 300 input samples; the resampler is buffering the whole stream")
	}
	// 300 in at 3:2 is exactly 200 out, and all of them are already available:
	// only the final interpolation partner is ever held back, never a whole
	// chunk. Emitting fewer would mean latency is being deferred.
	if got := r.OutSamples(); got != 200 {
		t.Fatalf("emitted %d output samples from 300 in, want 200 — output is being held back", got)
	}
}
