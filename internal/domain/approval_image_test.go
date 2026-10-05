package domain

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/jpeg"
	"strings"
	"testing"
)

func encodedJPEG(t *testing.T, width, height int) []byte {
	t.Helper()
	var buffer bytes.Buffer
	if err := jpeg.Encode(&buffer, image.NewGray(image.Rect(0, 0, width, height)), nil); err != nil {
		t.Fatal(err)
	}
	return buffer.Bytes()
}

// withDeclaredSize rewrites the frame header of a baseline JPEG, so the file
// claims dimensions its few bytes could never hold.
func withDeclaredSize(t *testing.T, data []byte, width, height int) []byte {
	t.Helper()
	data = bytes.Clone(data)
	index := bytes.Index(data, []byte{0xFF, 0xC0})
	if index < 0 {
		t.Fatal("fixture has no baseline frame header")
	}
	data[index+5], data[index+6] = byte(height>>8), byte(height)
	data[index+7], data[index+8] = byte(width>>8), byte(width)
	return data
}

func TestDecodeApprovalImageAcceptsAJPEGThatDecodes(t *testing.T) {
	data := encodedJPEG(t, 1280, 720)
	decoded, err := DecodeApprovalImage(ApprovalImagePrefix + base64.StdEncoding.EncodeToString(data))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(decoded, data) {
		t.Fatal("decoded bytes differ from the image sent")
	}
}

func TestDecodeApprovalImageRefusesWhatABrowserCannotShow(t *testing.T) {
	whole := encodedJPEG(t, 64, 36)
	cases := []struct {
		name string
		data []byte
		want string
	}{
		{"signature only", []byte{0xFF, 0xD8, 0xFF}, "JPEG"},
		{"truncated", whole[:len(whole)/2], "JPEG"},
		{"declares a canvas too large to show", withDeclaredSize(t, whole, MaxApprovalImageSide+1, 36), "pixels"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := DecodeApprovalImage(ApprovalImagePrefix + base64.StdEncoding.EncodeToString(tc.data))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want one mentioning %q", err, tc.want)
			}
		})
	}
}
