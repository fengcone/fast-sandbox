package idgen

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
)

// GenerateHashID creates a sandboxID from name, namespace, and timestamp
// Returns a 32-character md5 hex string
func GenerateHashID(name, namespace string, timestamp int64) string {
	data := fmt.Sprintf("%s:%s:%d", name, namespace, timestamp)
	hash := md5.Sum([]byte(data))
	return hex.EncodeToString(hash[:])
}
