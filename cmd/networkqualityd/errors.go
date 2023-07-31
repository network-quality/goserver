package main

import "errors"

var (
	errUnsupportedPlatform = errors.New("platform not supported") // Replace with errors.ErrUnsupported with 1.21+
)
