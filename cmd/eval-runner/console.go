package main

import (
	"io"
	"os"
	"syscall"
	"time"
)

type evaluationPipe struct{ file *os.File }

func (p evaluationPipe) Write(data []byte) (int, error) {
	if err := p.file.SetWriteDeadline(time.Now().Add(30 * time.Second)); err != nil {
		return 0, err
	}
	return p.file.Write(data)
}

func evaluationConsole() (io.Writer, func(), error) {
	info, err := os.Stdout.Stat()
	if err != nil {
		return nil, func() {}, err
	}
	if info.Mode()&os.ModeNamedPipe == 0 {
		return os.Stdout, func() {}, nil
	}
	// Reopen the pipe to make writes pollable without changing the parent's descriptor flags.
	file, err := os.OpenFile("/proc/self/fd/1", os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, func() {}, err
	}
	return evaluationPipe{file}, func() { _ = file.Close() }, nil
}
