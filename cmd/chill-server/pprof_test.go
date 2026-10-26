package main

import (
	"net"
	"net/http"
	"testing"
	"time"
)

func TestPprofOptions(t *testing.T) {
	for _, address := range []string{"", "127.0.0.1:6060", "[::1]:6060"} {
		if err := (options{pprofAddress: address}).validatePprof(); err != nil {
			t.Errorf("%q: %v", address, err)
		}
	}
	for _, opts := range []options{
		{pprofAddress: ":6060"}, {pprofAddress: "0.0.0.0:6060"},
		{pprofAddress: "192.0.2.1:6060"}, {pprofAddress: "6060"},
		{pprofAddress: "127.0.0.1:0"}, {pprofAddress: "127.0.0.1:65536"},
		{pprofAddress: "localhost:6060"}, {pprofMutexFraction: 1},
		{pprofBlockRate: time.Microsecond}, {pprofMutexFraction: -1},
		{pprofBlockRate: -1},
	} {
		if err := opts.validatePprof(); err == nil {
			t.Errorf("accepted unsafe options: %+v", opts)
		}
	}
}

func TestPprofListener(t *testing.T) {
	reservation, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := reservation.Addr().String()
	_ = reservation.Close()
	errors := make(chan error, 1)
	closeServer, err := startPprof(options{
		pprofAddress: address, pprofMutexFraction: 100, pprofBlockRate: time.Microsecond,
	}, errors)
	if err != nil {
		t.Fatal(err)
	}
	defer closeServer()
	client := &http.Client{Timeout: 5 * time.Second}
	for _, path := range []string{
		"/debug/pprof/", "/debug/pprof/heap", "/debug/pprof/allocs", "/debug/pprof/mutex", "/debug/pprof/block",
	} {
		response, err := client.Get("http://" + address + path)
		if err != nil {
			t.Fatal(err)
		}
		_ = response.Body.Close()
		if response.StatusCode != http.StatusOK {
			t.Errorf("%s: status %d", path, response.StatusCode)
		}
	}
}
