//go:build linux

package main

import (
	"errors"
	"os/user"
	"strconv"
)

var errGuardIdentityUnavailable = errors.New("guard identity is unavailable")

type guardIdentity struct {
	uid uint32
	gid uint32
}

func lookupGuardIdentity(lookup func(string) (*user.User, error)) (guardIdentity, error) {
	if lookup == nil {
		return guardIdentity{}, errGuardIdentityUnavailable
	}
	account, err := lookup("guard")
	if err != nil || account == nil {
		return guardIdentity{}, errGuardIdentityUnavailable
	}
	uid, err := parseIdentityNumber(account.Uid)
	if err != nil {
		return guardIdentity{}, errGuardIdentityUnavailable
	}
	gid, err := parseIdentityNumber(account.Gid)
	if err != nil {
		return guardIdentity{}, errGuardIdentityUnavailable
	}
	return guardIdentity{uid: uid, gid: gid}, nil
}

func parseIdentityNumber(value string) (uint32, error) {
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		return 0, err
	}
	return uint32(parsed), nil
}
