package main

import (
	"strings"
	"testing"

	"github.com/hellofresh/health-go/v5"
)

func TestRedactedURI(t *testing.T) {
	tests := []struct {
		name string
		uri  string
		want string
	}{
		{name: "password masked", uri: "postgresql://user:secretpw@host:5432/db", want: "postgresql://user:xxxxx@host:5432/db"},
		{name: "no credentials unchanged", uri: "https://example.com/health", want: "https://example.com/health"},
		{name: "unparseable with userinfo masked", uri: "://user:secretpw@host/db", want: "xxx@host/db"},
		{name: "unparseable without userinfo unchanged", uri: "://host/db", want: "://host/db"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := redactedURI(tt.uri); got != tt.want {
				t.Errorf("redactedURI(%q) = %q, want %q", tt.uri, got, tt.want)
			}
		})
	}
}

func TestRegisterRedactsPasswordInErrors(t *testing.T) {
	h, err := health.New()
	if err != nil {
		t.Fatalf("health.New: %v", err)
	}

	// Wrong scheme with an embedded password: the startup error must not
	// print the password into the logs.
	pg := &PostgreSQL{}

	err = Register(pg, "postpresql://user:secretpw@host:5432/db", h, &Config{})
	if err == nil {
		t.Fatal("Register() error = nil, want scheme error")
	}

	if strings.Contains(err.Error(), "secretpw") {
		t.Errorf("Register() error leaks password: %s", err)
	}

	if !strings.Contains(err.Error(), "user:xxxxx@host:5432") {
		t.Errorf("Register() error does not identify the redacted target: %s", err)
	}
}
