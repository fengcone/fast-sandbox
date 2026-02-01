package idgen

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestGenerateHashID(t *testing.T) {
	name := "test-sb"
	namespace := "default"
	timestamp := int64(1234567890123456789)

	id := GenerateHashID(name, namespace, timestamp)

	// Should be 32 character hex string (md5)
	assert.Len(t, id, 32, "MD5 hash should be 32 characters")

	// Same inputs should produce same output
	id2 := GenerateHashID(name, namespace, timestamp)
	assert.Equal(t, id, id2, "Same inputs should produce same hash")

	// Different timestamp should produce different output
	id3 := GenerateHashID(name, namespace, timestamp+1)
	assert.NotEqual(t, id, id3, "Different timestamp should produce different hash")

	// Different namespace should produce different output
	id4 := GenerateHashID(name, "other", timestamp)
	assert.NotEqual(t, id, id4, "Different namespace should produce different hash")
}
