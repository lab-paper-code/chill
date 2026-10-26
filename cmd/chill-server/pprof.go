package main

import (
	"errors"
	"net"
	"net/http"
	"net/http/pprof"
	"runtime"
	"strconv"
	"time"
)

func (o options) validatePprof() error {
	if o.pprofMutexFraction < 0 || o.pprofBlockRate < 0 {
		return errors.New("pprof sampling rates must be nonnegative")
	}
	if o.pprofAddress == "" {
		if o.pprofMutexFraction != 0 || o.pprofBlockRate != 0 {
			return errors.New("pprof sampling requires pprof-address")
		}
		return nil
	}
	host, port, err := net.SplitHostPort(o.pprofAddress)
	if err != nil {
		return errors.New("pprof-address must be an explicit loopback host:port")
	}
	ip := net.ParseIP(host)
	p, portErr := strconv.Atoi(port)
	if ip == nil || !ip.IsLoopback() || portErr != nil || p < 1 || p > 65535 {
		return errors.New("pprof-address requires a loopback IP and a port between 1 and 65535")
	}
	return nil
}

// A private mux keeps profiling routes off the serving listener.
func startPprof(o options, serverErrors chan<- error) (func(), error) {
	if err := o.validatePprof(); err != nil {
		return nil, err
	}
	if o.pprofAddress == "" {
		return func() {}, nil
	}
	listener, err := net.Listen("tcp", o.pprofAddress)
	if err != nil {
		return nil, err
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/debug/pprof/", pprof.Index)
	mux.HandleFunc("/debug/pprof/cmdline", pprof.Cmdline)
	mux.HandleFunc("/debug/pprof/profile", pprof.Profile)
	mux.HandleFunc("/debug/pprof/symbol", pprof.Symbol)
	mux.HandleFunc("/debug/pprof/trace", pprof.Trace)
	server := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	previousMutexFraction := runtime.SetMutexProfileFraction(o.pprofMutexFraction)
	runtime.SetBlockProfileRate(int(o.pprofBlockRate))
	go func() { serverErrors <- server.Serve(listener) }()
	return func() {
		_ = server.Close()
		runtime.SetMutexProfileFraction(previousMutexFraction)
		runtime.SetBlockProfileRate(0)
	}, nil
}
