package main

import (
	"fmt"
	"strings"
	"time"
)

// parseDay converts a day input into a time. It accepts dates in the
// dd-mm-yyyy layout used on the command line, plus the relative values
// "yesterday" and "today" resolved against now. An empty value yields the
// zero time, meaning no day is set.
func parseDay(value string, now time.Time) (time.Time, error) {
	switch strings.ToLower(value) {
	case "":
		return time.Time{}, nil
	case "yesterday":
		return now.AddDate(0, 0, -1), nil
	case "today":
		return now, nil
	}

	t, err := time.Parse(inputDayFormat, value)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid day %q: %w", value, err)
	}

	return t, nil
}
