package referencedata

import (
	"crypto/sha256"
	"encoding/hex"
	"io/fs"
)

func fsRead(source fs.FS, name string) ([]byte, error) {
	return fs.ReadFile(source, name)
}

func checksumOf(body []byte) string {
	sum := sha256.Sum256(body)
	return hex.EncodeToString(sum[:])
}
