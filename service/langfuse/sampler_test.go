package langfuse

import (
	"crypto/sha256"
	"encoding/binary"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// digestWithBucket returns a digest function that pins the first eight bytes to
// bucket, which is the only part of the digest the sampler is allowed to read.
func digestWithBucket(bucket uint64, tail byte) func([]byte) [32]byte {
	return func([]byte) [32]byte {
		var sum [32]byte
		binary.BigEndian.PutUint64(sum[0:8], bucket)
		for i := 8; i < len(sum); i++ {
			sum[i] = tail
		}
		return sum
	}
}

func TestSamplerUsesExactIntegerThreshold(t *testing.T) {
	// float64(0.1) is 3602879701896397 * 2^-55, so floor(0.1 * 2^64) is exactly
	// 3602879701896397 * 512 with no rounding involved.
	const tenPercentThreshold = uint64(1844674407370955264)

	cases := []struct {
		name   string
		bucket uint64
		rate   float64
		hit    bool
	}{
		{name: "half rate just below threshold", bucket: 1<<63 - 1, rate: 0.5, hit: true},
		{name: "half rate at threshold", bucket: 1 << 63, rate: 0.5, hit: false},
		{name: "tenth rate just below threshold", bucket: tenPercentThreshold - 1, rate: 0.1, hit: true},
		{name: "tenth rate at threshold", bucket: tenPercentThreshold, rate: 0.1, hit: false},
		// floor(1e-18 * 2^64) is 18, so the first 18 buckets are the entire sample.
		{name: "tiny rate keeps its leading buckets", bucket: 17, rate: 1e-18, hit: true},
		{name: "tiny rate stops at its threshold", bucket: 18, rate: 1e-18, hit: false},
		// A rate whose threshold floors to zero samples nothing rather than
		// degenerating into an always-hit.
		{name: "sub bucket rate samples nothing", bucket: 0, rate: math.SmallestNonzeroFloat64, hit: false},
		// The largest float64 below 1 is 1-2^-53, so the threshold is 2^64-2^11
		// and the top 2047 buckets still miss.
		{name: "max bucket misses the largest rate below one", bucket: math.MaxUint64, rate: math.Nextafter(1, 0), hit: false},
		{name: "threshold edge of the largest rate below one", bucket: 1<<64 - 1<<11 - 1, rate: math.Nextafter(1, 0), hit: true},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			assert.Equal(t, tc.hit, sampleHit("key", tc.rate, digestWithBucket(tc.bucket, 0xff)))
		})
	}
}

func TestSamplerReadsOnlyTheLeadingBigEndianBucket(t *testing.T) {
	// Bytes after the first eight are saturated in one call and zero in the
	// other; a sampler reading anything but the leading big-endian uint64 would
	// disagree between them.
	low := sampleHit("key", 0.5, digestWithBucket(1, 0xff))
	high := sampleHit("key", 0.5, digestWithBucket(1, 0x00))
	assert.True(t, low)
	assert.True(t, high)

	assert.False(t, sampleHit("key", 0.5, digestWithBucket(math.MaxUint64, 0x00)))
}

func TestSamplerAcceptsEverythingAtFullRate(t *testing.T) {
	for _, key := range []string{"", "req-1", "42:session"} {
		assert.True(t, sampleHit(key, 1, sha256.Sum256), "key %q", key)
	}
}

func TestSamplingKeyPrefersScopedSessionOverRequestID(t *testing.T) {
	scoped := SessionIdentity{ScopedID: "42:default", RawSessionID: "default", Source: "x-langfuse-session-id"}
	assert.Equal(t, "42:default", samplingKeyFor(scoped, "req-1"))
	assert.Equal(t, "42:default", samplingKeyFor(scoped, "req-2"))

	// A raw session that could not be scoped must not leak into the key.
	unscoped := SessionIdentity{RawSessionID: "default", Source: "x-langfuse-session-id"}
	assert.Equal(t, "req-1", samplingKeyFor(unscoped, "req-1"))
	assert.Equal(t, "req-1", samplingKeyFor(SessionIdentity{}, "req-1"))
}

func TestSamplingKeySeparatesUsersSharingOneRawSession(t *testing.T) {
	first, ok := scopeSession(41, "default")
	require.True(t, ok)
	second, ok := scopeSession(42, "default")
	require.True(t, ok)

	firstKey := samplingKeyFor(SessionIdentity{ScopedID: first}, "req-1")
	secondKey := samplingKeyFor(SessionIdentity{ScopedID: second}, "req-2")
	assert.NotEqual(t, firstKey, secondKey)

	// The same scoped session decides identically no matter the request ID.
	assert.Equal(t,
		sampleHit(firstKey, 0.3, sha256.Sum256),
		sampleHit(samplingKeyFor(SessionIdentity{ScopedID: first}, "req-9"), 0.3, sha256.Sum256))
}
