package core

import (
	"errors"
	"net/netip"
)

// ErrManagedPolicyUninitialized reports the sole recoverable absence of a
// complete persisted managed Policy. Callers must not use it for malformed or
// partially persisted Policy rows.
var ErrManagedPolicyUninitialized = errors.New("managed policy is uninitialized")

// NewInitialManagedPolicyIntent returns the contract-defined first persisted
// Policy. Dynamic Policy facts remain SQLite-owned after this one-time
// bootstrap; runtime configuration is not a Policy input.
func NewInitialManagedPolicyIntent() (ManagedPolicyIntent, error) {
	return NewManagedPolicyIntent(nil, []netip.Prefix{
		netip.MustParsePrefix("127.0.0.0/8"),
		netip.MustParsePrefix("::1/128"),
	})
}
