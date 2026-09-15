// Package fnv holds the FNV-1a hash shared by the public event-name API and
// room sharding. The wire ID and the internal shard must use the same hash.
package fnv

const (
	offset32 = 2166136261
	prime32  = 16777619
)

// Hash32 returns the FNV-1a 32-bit hash of s.
func Hash32(s string) uint32 {
	h := uint32(offset32)
	for i := 0; i < len(s); i++ {
		h ^= uint32(s[i])
		h *= prime32
	}
	return h
}

// Hash32Bytes returns the FNV-1a 32-bit hash of b.
func Hash32Bytes(b []byte) uint32 {
	h := uint32(offset32)
	for _, c := range b {
		h ^= uint32(c)
		h *= prime32
	}
	return h
}
