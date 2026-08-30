package decision

import (
	"errors"
	"testing"
)

func TestCommitUnknownErrorHasStableClassificationAndCause(t *testing.T) {
	cause := errors.New("injected commit failure")
	err := NewCommitUnknownError(cause)
	if !errors.Is(err, ErrCommitUnknown) {
		t.Fatalf("commit error classification = %v", err)
	}
	var typed *CommitUnknownError
	if !errors.As(err, &typed) || typed.Cause != cause {
		t.Fatalf("typed commit error = %#v", typed)
	}
}
