//go:build lintcheck

// This file exists to prove the no-network depguard gate bites: it is
// excluded from normal builds by the lintcheck tag and only linted by
// TestNoNetworkGateBites, which asserts golangci-lint rejects it.
package lintcheck

import "net/http"

var _ = http.DefaultClient
