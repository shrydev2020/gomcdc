package cli

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
)

func newRunID() (string, error) {
	return readRunID(rand.Reader)
}

func readRunID(reader io.Reader) (string, error) {
	var value [16]byte
	if _, err := io.ReadFull(reader, value[:]); err != nil {
		return "", fmt.Errorf("generate coverage run ID: %w", err)
	}
	return hex.EncodeToString(value[:]), nil
}
