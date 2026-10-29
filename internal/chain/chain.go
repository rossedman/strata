// Package chain computes OCI layer chain IDs.
//
// A chain ID identifies an ordered stack of uncompressed layers. The OCI image
// spec defines it recursively:
//
//	chain(1) = diff_id(1)
//	chain(n) = sha256(chain(n-1) + " " + diff_id(n))
//
// Two images share a chain ID at depth n exactly when their first n layers
// are identical, which is what strata uses to infer parents.
package chain

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"strings"
)

// IDs returns the chain ID of every prefix of diffIDs, shortest first, so
// IDs(d)[len(d)-1] is the image's top chain ID. Every diff ID must be a
// well-formed sha256 digest.
func IDs(diffIDs []string) ([]string, error) {
	out := make([]string, len(diffIDs))
	buf := make([]byte, 0, 2*(7+64)+1)
	for i, d := range diffIDs {
		if err := Validate(d); err != nil {
			return nil, fmt.Errorf("diff_id %d: %w", i, err)
		}
		if i == 0 {
			out[i] = d
			continue
		}
		buf = append(buf[:0], out[i-1]...)
		buf = append(buf, ' ')
		buf = append(buf, d...)
		sum := sha256.Sum256(buf)
		out[i] = "sha256:" + hex.EncodeToString(sum[:])
	}
	return out, nil
}

// Top returns the chain ID of the full stack, or "" for an image with no
// layers.
func Top(diffIDs []string) (string, error) {
	ids, err := IDs(diffIDs)
	if err != nil || len(ids) == 0 {
		return "", err
	}
	return ids[len(ids)-1], nil
}

// Validate reports whether d is a lowercase sha256 digest in the form
// "sha256:<64 hex>".
func Validate(d string) error {
	hexPart, ok := strings.CutPrefix(d, "sha256:")
	if !ok {
		return fmt.Errorf("%q is not a sha256 digest", d)
	}
	if len(hexPart) != 64 {
		return fmt.Errorf("%q has %d hex characters, want 64", d, len(hexPart))
	}
	for _, c := range hexPart {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return fmt.Errorf("%q contains a non-hex character", d)
		}
	}
	return nil
}
