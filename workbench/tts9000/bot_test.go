package main

import (
	"testing"
)

func TestIsAllowedUser(t *testing.T) {
	tests := []struct {
		name     string
		allowed  []string
		userID   int64
		expected bool
	}{
		{"empty list allows everyone", nil, 123, true},
		{"user in list", []string{"111", "222"}, 222, true},
		{"user not in list", []string{"111", "222"}, 333, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isAllowedUser(tt.allowed, tt.userID); got != tt.expected {
				t.Errorf("isAllowedUser(%v, %d) = %v, want %v", tt.allowed, tt.userID, got, tt.expected)
			}
		})
	}
}

func TestIsValidURL(t *testing.T) {
	tests := []struct {
		name     string
		text     string
		expected bool
	}{
		{"https url", "https://example.com/article", true},
		{"http url", "http://example.com", true},
		{"no scheme", "example.com/article", false},
		{"ftp scheme", "ftp://example.com", false},
		{"plain text", "hello there", false},
		{"empty", "", false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isValidURL(tt.text); got != tt.expected {
				t.Errorf("isValidURL(%q) = %v, want %v", tt.text, got, tt.expected)
			}
		})
	}
}
