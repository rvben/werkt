package main

import (
	"errors"
	"testing"

	"github.com/rvben/werkt/internal/authoring"
)

func TestDraftCommandIsDisabledBeforeReadingIntentWithoutBYOKKey(t *testing.T) {
	t.Setenv("TYPESAFE_API_KEY", "")
	if err := draftCommand(nil); !errors.Is(err, authoring.ErrDisabled) {
		t.Fatalf("error = %v, want ErrDisabled", err)
	}
}
