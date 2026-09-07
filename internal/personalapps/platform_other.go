//go:build !darwin

package personalapps

// platformSupported is false everywhere but macOS. A non-Darwin build keeps
// normal chat behavior, exposes no callable personal-app operation beyond
// status, and reports a clear diagnostic when one is requested anyway.
func platformSupported() bool { return false }
