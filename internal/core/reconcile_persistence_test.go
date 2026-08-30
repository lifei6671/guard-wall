package core

import (
	"errors"
	"testing"
)

func TestReconcileCommitUnknownErrorHasStableClassificationAndCause(t *testing.T) {
	cause := errors.New("lost commit acknowledgement")
	err := NewReconcileCommitUnknownError(cause)
	if !errors.Is(err, ErrReconcileCommitUnknown) {
		t.Fatalf("error = %v, want ErrReconcileCommitUnknown", err)
	}
	var typed *ReconcileCommitUnknownError
	if !errors.As(err, &typed) || typed.Cause != cause {
		t.Fatalf("typed commit-unknown error = %#v", typed)
	}
}
