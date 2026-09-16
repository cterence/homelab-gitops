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

func SplitRange(min, max, divisions int) [][2]int {
	if divisions <= 0 {
		return nil
	}

	total := max - min + 1
	chunk := total / divisions
	remainder := total % divisions

	ranges := make([][2]int, 0, divisions)
	start := min

	for i := 0; i < divisions; i++ {
		size := chunk
		if i < remainder {
			size++ // distribute remainder
		}

		if size == 0 {
			break
		}

		end := start + size - 1
		ranges = append(ranges, [2]int{start, end})
		start = end + 1
	}

	return ranges
}
