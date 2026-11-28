package main

import "testing"

func TestResolveZone(t *testing.T) {
	tests := []struct {
		name    string
		zone    string
		want    string
		wantErr bool
	}{
		{name: "explicit IANA name", zone: "Europe/Paris", want: "Europe/Paris"},
		{name: "UTC is a real name", zone: "UTC", want: "UTC"},
		{name: "unknown name is a usage error", zone: "Mars/Base", wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			zone, err := resolveZone(tt.zone)

			if tt.wantErr {
				if err == nil {
					t.Fatalf("expected an error for %q", tt.zone)
				}

				if code := exitCode(err); code != 2 {
					t.Fatalf("exit code = %d, want 2", code)
				}

				return
			}

			if err != nil {
				t.Fatalf("resolveZone: %v", err)
			}

			if zone.String() != tt.want {
				t.Fatalf("zone = %q, want %q", zone.String(), tt.want)
			}
		})
	}
}
