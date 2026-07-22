package main

import (
	"os"
	"syscall"
)

type channelRestarter struct {
	ch chan<- string
}

func (r channelRestarter) RequestRestart(reason string) {
	select {
	case r.ch <- reason:
	default:
	}
}

func reexecSelf() error {
	executable, err := os.Executable()
	if err != nil {
		executable = os.Args[0]
	}
	return syscall.Exec(executable, os.Args, os.Environ())
}
