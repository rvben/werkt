package domain

import (
	"bytes"
	"encoding/base64"
	"errors"
	"fmt"
	"image/jpeg"
	"strings"
)

// ApprovalImagePrefix is the only accepted spelling of an image field's value:
// an inline JPEG, so the reviewer sees the evidence without Werkt fetching
// anything on the automation's behalf.
const ApprovalImagePrefix = "data:image/jpeg;base64,"

// MaxApprovalImageBytes bounds one decoded image, and MaxApprovalImagesBytes
// all images of one approval, so a request stays small enough to store with
// the approval and to load in a phone browser.
const (
	MaxApprovalImageBytes  = 256 << 10
	MaxApprovalImagesBytes = 1 << 20
)

// MaxApprovalImageSide bounds an image's declared width and height, which
// decoding allocates for whatever the file's size.
const MaxApprovalImageSide = 4096

// DecodeApprovalImage returns the JPEG bytes an image field carries, refusing
// anything that is not a strictly encoded JPEG within MaxApprovalImageBytes.
func DecodeApprovalImage(value any) ([]byte, error) {
	text, ok := value.(string)
	if !ok {
		return nil, errors.New("approval image value must be a string")
	}
	encoded, ok := strings.CutPrefix(text, ApprovalImagePrefix)
	if !ok {
		return nil, fmt.Errorf("approval image value must start with %q", ApprovalImagePrefix)
	}
	if base64.StdEncoding.DecodedLen(len(encoded)) > MaxApprovalImageBytes+2 {
		return nil, fmt.Errorf("approval image must be at most %d bytes", MaxApprovalImageBytes)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(encoded)
	if err != nil {
		return nil, errors.New("approval image value must be valid base64")
	}
	if len(decoded) > MaxApprovalImageBytes {
		return nil, fmt.Errorf("approval image must be at most %d bytes", MaxApprovalImageBytes)
	}
	// The header is read first so a few bytes declaring a vast canvas are
	// refused before decoding allocates it; the full decode then refuses a
	// file that is truncated or corrupt, which would show as a broken image.
	config, err := jpeg.DecodeConfig(bytes.NewReader(decoded))
	if err != nil {
		return nil, fmt.Errorf("approval image must be a JPEG: %w", err)
	}
	if config.Width > MaxApprovalImageSide || config.Height > MaxApprovalImageSide {
		return nil, fmt.Errorf("approval image must be at most %d pixels on each side", MaxApprovalImageSide)
	}
	if _, err := jpeg.Decode(bytes.NewReader(decoded)); err != nil {
		return nil, fmt.Errorf("approval image must be a JPEG that decodes: %w", err)
	}
	return decoded, nil
}
