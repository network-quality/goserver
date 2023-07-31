// Copyright (c) 2021-2023 Apple Inc. Licensed under MIT License.

//go:build darwin
// +build darwin

package main

import (
	"syscall"
)

func setTCPL4S(syscall.RawConn, string) error {
	return errUnsupportedPlatform
}
