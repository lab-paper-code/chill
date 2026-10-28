//go:build gatewaycontrol

package main

// A distinct build target, not a default-off rollout switch in E5's executable.
const serverRole = controlRole
