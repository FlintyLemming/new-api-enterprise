package langfuse

import (
	"encoding/binary"
	"math/big"
)

// sampleHit decides whether one request is traced. The mapping is fixed by
// design §5.1: bucket is the big-endian uint64 of the first eight digest bytes
// and the threshold is the exact floor(rate * 2^64) of the validated IEEE-754
// rate. The comparison stays in integer space on purpose — converting the
// bucket to float64 would collapse its low 11 bits and make rates near the
// boundary decide the wrong way. The digest is injected so tests can pin a
// bucket instead of searching for a preimage.
func sampleHit(samplingKey string, rate float64, digest func([]byte) [32]byte) bool {
	if rate >= 1 {
		return true
	}
	if rate <= 0 {
		// Phase 0 already rejected these; the guard only keeps the sampler
		// total when it is called directly.
		return false
	}

	sum := digest([]byte(samplingKey))
	bucket := new(big.Int).SetUint64(binary.BigEndian.Uint64(sum[0:8]))

	scaled := new(big.Float).SetPrec(128).SetFloat64(rate)
	scaled.Mul(scaled, new(big.Float).SetPrec(128).SetInt(new(big.Int).Lsh(big.NewInt(1), 64)))
	threshold, _ := scaled.Int(nil) // positive value, so Int truncates toward floor

	return bucket.Cmp(threshold) < 0
}

// samplingKeyFor picks the identity a sampling decision is stable across. A
// user scoped session keeps a whole conversation in or out of the sample; every
// other request is decided per request ID. The raw session is never used, so
// two users sending the same client value are sampled independently.
func samplingKeyFor(session SessionIdentity, requestId string) string {
	if session.ScopedID != "" {
		return session.ScopedID
	}
	return requestId
}
