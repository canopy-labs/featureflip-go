package featureflip

import (
	"crypto/md5"
	"encoding/binary"
)

// bucket computes a deterministic bucket in [0, 99] for a given salt and value.
// Used for percentage rollout flag evaluation.
func bucket(salt, value string) int {
	h := md5.Sum([]byte(salt + ":" + value))
	n := binary.LittleEndian.Uint32(h[:4])
	return int(n % 100)
}
