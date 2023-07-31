// Copyright (c) 2021-2023 Apple Inc. Licensed under MIT License.

//go:build windows
// +build windows

package main

import (
	"syscall"
)

func setTCPNotSentLowat(conn syscall.RawConn, value int) error {
	return errUnsupportedPlatform
}

func setTCPL4S(conn syscall.RawConn, value string) error {
	return errUnsupportedPlatform
}

func setIPTos(network string, conn syscall.RawConn, value int) error {
	return errUnsupportedPlatform
}
