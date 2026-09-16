package main

import (
	"log/slog"
	"os"
)

func CloseFile(f *os.File) {
	if err := f.Close(); err != nil {
		slog.Error(err.Error())
	}
}
