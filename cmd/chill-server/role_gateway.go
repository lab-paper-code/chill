//go:build !gatewaycontrol

package main

// The measured E5 executable retains its original composition. Build the
// separate chill-control executable with -tags gatewaycontrol for split trials.
const serverRole = "gateway"
