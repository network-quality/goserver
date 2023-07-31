// Copyright (c) 2021-2023 Apple Inc. Licensed under MIT License.

//go:build linux
// +build linux

package main

import (
	"syscall"

	"golang.org/x/sys/unix"
)

func setTCPL4S(conn syscall.RawConn, value string) error {
	var setsockoptErr error
	if err := conn.Control(func(fd uintptr) {
		setsockoptErr = syscall.SetsockoptString(int(fd), unix.IPPROTO_TCP, unix.TCP_CONGESTION, value)
	}); err != nil {
		return err
	}
	return setsockoptErr
}
