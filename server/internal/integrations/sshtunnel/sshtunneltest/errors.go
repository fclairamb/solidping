package sshtunneltest

import "errors"

// errAuthFailed is the generic auth rejection the fake bastion returns; SSH
// deliberately does not tell a client which half of the credential was wrong.
var errAuthFailed = errors.New("authentication failed")
