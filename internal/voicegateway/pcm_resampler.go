package voicegateway

import "encoding/binary"

// pcmResampler converts the vendor's output rate to the client's playback rate
// incrementally, one vendor chunk at a time.
//
// The batch resampler cannot simply be applied per chunk. At 3:2, output sample
// i interpolates between input samples floor(1.5*i) and floor(1.5*i)+1, and a
// vendor chunk boundary almost never lands on that boundary. Resampling each
// chunk on its own would restart the interpolation phase at every seam — drift,
// plus a click at each one.
//
// The contract, and the test that holds it: feeding a stream through Write in
// *any* chunking yields byte-for-byte what feeding it as one piece yields.
//
// That equality is possible only because output sample i depends on nothing but
// input samples floor(1.5*i) and floor(1.5*i)+1 — never on the total length. So
// a sample can be emitted the moment both its sources are in hand, and when the
// stream ends exactly outCount samples have gone out. **There is nothing to
// flush.** See TestResamplerOutputDoesNotDependOnTotalLength, which is what
// pins that premise: if the upstream clamp `hi >= inCount` ever started firing,
// output would depend on the total and this type could not exist.
type pcmResampler struct {
	pending  []byte // input not yet consumed, s16le
	base     int64  // absolute sample index of pending[0]
	outIndex int64  // absolute index of the next output sample to emit
}

const (
	resampleInSamples  = duplexOutputRate / 8000   // 3
	resampleOutSamples = clientPlaybackRate / 8000 // 2
)

// Write appends one vendor chunk and returns whatever output it completed.
// A trailing odd byte is carried to the next call, the same way the batch form
// drops one at the end of the stream.
func (r *pcmResampler) Write(chunk []byte) []byte {
	r.pending = append(r.pending, chunk...)
	totalIn := r.base + int64(len(r.pending)/2)

	var out []byte
	for {
		// Integer form of floor(1.5 * outIndex) for non-negative indices.
		lo := r.outIndex * resampleInSamples / resampleOutSamples
		hi := lo + 1
		// Both sources must be present before the sample can be produced.
		// hi is the later one; the upstream clamp never applies (see the doc
		// comment), so waiting on hi is sufficient.
		if hi > totalIn-1 {
			break
		}
		out = append(out, r.interpolate(lo, hi)...)
		r.outIndex++
	}

	// Drop input no later sample can need. Invariant under trimming:
	// base + len(pending)/2 stays equal to totalIn.
	if next := r.outIndex * resampleInSamples / resampleOutSamples; next > r.base {
		r.pending = r.pending[(next-r.base)*2:]
		r.base = next
	}
	return out
}

// OutSamples reports how many output samples have been produced.
func (r *pcmResampler) OutSamples() int64 { return r.outIndex }

// interpolate renders one output sample from absolute input indices lo and hi.
//
// The float arithmetic is deliberately the same shape as the batch form's:
// outIndex*3/2 is exactly representable, and the fraction is always 0 or 0.5,
// so this reproduces the batch result bit for bit rather than approximately.
func (r *pcmResampler) interpolate(lo, hi int64) []byte {
	pos := float64(r.outIndex) * float64(resampleInSamples) / float64(resampleOutSamples)
	a := float64(int16(binary.LittleEndian.Uint16(r.pending[(lo-r.base)*2:])))
	b := float64(int16(binary.LittleEndian.Uint16(r.pending[(hi-r.base)*2:])))
	sample := int16(a + (b-a)*(pos-float64(lo)))
	buf := make([]byte, 2)
	binary.LittleEndian.PutUint16(buf, uint16(sample))
	return buf
}
