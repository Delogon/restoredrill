// Package ulid implements Universally Unique Lexicographically Sortable
// Identifiers (ULIDs) using only the standard library. Run IDs are ULIDs so
// they sort by creation time and are safe to use in file names and URLs.
package ulid

import (
	"crypto/rand"
	"fmt"
	"strings"
	"sync"
	"time"
)

// crockford is the Crockford base32 alphabet used by ULIDs (no I, L, O, U).
const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

// EncodedSize is the length of a ULID string.
const EncodedSize = 26

var (
	mu      sync.Mutex
	lastMS  uint64
	lastRaw [16]byte
)

// New returns a new ULID for the current time. IDs are monotonic within the
// same millisecond so a burst of IDs keeps its creation order.
func New() string {
	return NewAt(time.Now())
}

// NewAt returns a ULID encoding the given timestamp.
func NewAt(t time.Time) string {
	ms := uint64(t.UnixMilli())
	mu.Lock()
	defer mu.Unlock()

	var raw [16]byte
	if ms == lastMS {
		raw = lastRaw
		// Increment the whole 128-bit value, big endian.
		for i := 15; i >= 0; i-- {
			raw[i]++
			if raw[i] != 0 {
				break
			}
		}
	} else {
		raw[0] = byte(ms >> 40)
		raw[1] = byte(ms >> 32)
		raw[2] = byte(ms >> 24)
		raw[3] = byte(ms >> 16)
		raw[4] = byte(ms >> 8)
		raw[5] = byte(ms)
		if _, err := rand.Read(raw[6:]); err != nil {
			// crypto/rand does not fail on supported platforms; panic is
			// preferable to silently producing predictable IDs.
			panic(fmt.Sprintf("ulid: crypto/rand failed: %v", err))
		}
	}
	lastMS, lastRaw = ms, raw
	return encode(raw)
}

// encode maps 16 bytes to 26 Crockford base32 characters. The first character
// carries only the top 3 bits; every following character carries 5 bits.
func encode(src [16]byte) string {
	var dst [EncodedSize]byte
	dst[0] = crockford[(src[0]&224)>>5]
	dst[1] = crockford[src[0]&31]
	dst[2] = crockford[(src[1]&248)>>3]
	dst[3] = crockford[((src[1]&7)<<2)|((src[2]&192)>>6)]
	dst[4] = crockford[(src[2]&62)>>1]
	dst[5] = crockford[((src[2]&1)<<4)|((src[3]&240)>>4)]
	dst[6] = crockford[((src[3]&15)<<1)|((src[4]&128)>>7)]
	dst[7] = crockford[(src[4]&124)>>2]
	dst[8] = crockford[((src[4]&3)<<3)|((src[5]&224)>>5)]
	dst[9] = crockford[src[5]&31]
	dst[10] = crockford[(src[6]&248)>>3]
	dst[11] = crockford[((src[6]&7)<<2)|((src[7]&192)>>6)]
	dst[12] = crockford[(src[7]&62)>>1]
	dst[13] = crockford[((src[7]&1)<<4)|((src[8]&240)>>4)]
	dst[14] = crockford[((src[8]&15)<<1)|((src[9]&128)>>7)]
	dst[15] = crockford[(src[9]&124)>>2]
	dst[16] = crockford[((src[9]&3)<<3)|((src[10]&224)>>5)]
	dst[17] = crockford[src[10]&31]
	dst[18] = crockford[(src[11]&248)>>3]
	dst[19] = crockford[((src[11]&7)<<2)|((src[12]&192)>>6)]
	dst[20] = crockford[(src[12]&62)>>1]
	dst[21] = crockford[((src[12]&1)<<4)|((src[13]&240)>>4)]
	dst[22] = crockford[((src[13]&15)<<1)|((src[14]&128)>>7)]
	dst[23] = crockford[(src[14]&124)>>2]
	dst[24] = crockford[((src[14]&3)<<3)|((src[15]&224)>>5)]
	dst[25] = crockford[src[15]&31]
	return string(dst[:])
}

// Time extracts the timestamp encoded in a ULID string.
func Time(s string) (time.Time, error) {
	raw, err := decode(s)
	if err != nil {
		return time.Time{}, err
	}
	ms := uint64(raw[0])<<40 | uint64(raw[1])<<32 | uint64(raw[2])<<24 |
		uint64(raw[3])<<16 | uint64(raw[4])<<8 | uint64(raw[5])
	return time.UnixMilli(int64(ms)), nil
}

// Valid reports whether s is a well-formed ULID.
func Valid(s string) bool {
	_, err := decode(s)
	return err == nil
}

func decode(s string) ([16]byte, error) {
	var raw [16]byte
	if len(s) != EncodedSize {
		return raw, fmt.Errorf("ulid: invalid length %d", len(s))
	}
	s = strings.ToUpper(s)
	// The first character encodes only 3 bits, so it must be <= 7.
	if s[0] > '7' {
		return raw, fmt.Errorf("ulid: overflow in first character")
	}
	// Accumulate 5 bits per character into the 128-bit big-endian value.
	var acc [16]byte
	for i := 0; i < EncodedSize; i++ {
		v := strings.IndexByte(crockford, s[i])
		if v < 0 {
			return raw, fmt.Errorf("ulid: invalid character %q", s[i])
		}
		// acc = acc<<5 | v
		carry := byte(v)
		for j := 15; j >= 0; j-- {
			next := acc[j] >> 3
			acc[j] = acc[j]<<5 | carry
			carry = next
		}
	}
	return acc, nil
}