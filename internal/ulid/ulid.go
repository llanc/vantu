package ulid

import (
	"crypto/rand"
	"sync"
	"time"
)

const crockford = "0123456789ABCDEFGHJKMNPQRSTVWXYZ"

var (
	mu       sync.Mutex
	lastMS   uint64
	lastRand [10]byte
)

// New generates a lexicographically sortable 26-character ULID string.
func New() string {
	mu.Lock()
	defer mu.Unlock()

	ms := uint64(time.Now().UnixMilli())
	if ms == lastMS {
		// Increment 80-bit random entropy monotonically within the same millisecond.
		for i := 9; i >= 0; i-- {
			lastRand[i]++
			if lastRand[i] != 0 {
				break
			}
		}
	} else {
		lastMS = ms
		_, _ = rand.Read(lastRand[:])
	}

	var out [26]byte
	// 48-bit timestamp -> 10 chars
	out[0] = crockford[(ms>>45)&0x1F]
	out[1] = crockford[(ms>>40)&0x1F]
	out[2] = crockford[(ms>>35)&0x1F]
	out[3] = crockford[(ms>>30)&0x1F]
	out[4] = crockford[(ms>>25)&0x1F]
	out[5] = crockford[(ms>>20)&0x1F]
	out[6] = crockford[(ms>>15)&0x1F]
	out[7] = crockford[(ms>>10)&0x1F]
	out[8] = crockford[(ms>>5)&0x1F]
	out[9] = crockford[ms&0x1F]

	// 80-bit randomness -> 16 chars
	r := lastRand
	out[10] = crockford[(r[0]>>3)&0x1F]
	out[11] = crockford[((r[0]&0x07)<<2)|((r[1]>>6)&0x03)]
	out[12] = crockford[(r[1]>>1)&0x1F]
	out[13] = crockford[((r[1]&0x01)<<4)|((r[2]>>4)&0x0F)]
	out[14] = crockford[((r[2]&0x0F)<<1)|((r[3]>>7)&0x01)]
	out[15] = crockford[(r[3]>>2)&0x1F]
	out[16] = crockford[((r[3]&0x03)<<3)|((r[4]>>5)&0x07)]
	out[17] = crockford[r[4]&0x1F]
	out[18] = crockford[(r[5]>>3)&0x1F]
	out[19] = crockford[((r[5]&0x07)<<2)|((r[6]>>6)&0x03)]
	out[20] = crockford[(r[6]>>1)&0x1F]
	out[21] = crockford[((r[6]&0x01)<<4)|((r[7]>>4)&0x0F)]
	out[22] = crockford[((r[7]&0x0F)<<1)|((r[8]>>7)&0x01)]
	out[23] = crockford[(r[8]>>2)&0x1F]
	out[24] = crockford[((r[8]&0x03)<<3)|((r[9]>>5)&0x07)]
	out[25] = crockford[r[9]&0x1F]

	return string(out[:])
}
