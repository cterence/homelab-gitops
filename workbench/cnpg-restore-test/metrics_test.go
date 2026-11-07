package main

import (
	"context"
	"net"
	"testing"
	"time"
)

func TestPushMetricsUnreachableCollector(t *testing.T) {
	t.Run("returns error when collector is unreachable", func(t *testing.T) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatalf("listening: %v", err)
		}

		addr := l.Addr().String()
		_ = l.Close()

		err = pushMetrics(context.Background(), Config{OTeleEndpoint: addr}, nil, time.Second, false)
		if err == nil {
			t.Fatal("pushMetrics() = nil, want error when metrics cannot be delivered")
		}
	})
}
