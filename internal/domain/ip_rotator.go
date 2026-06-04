package domain

// IPRotator is the port for changing the apparent exit IP used to
// reach upstream providers. Typically backed by a Tor controller.
type IPRotator interface {
	// NewIP requests a new exit IP, subject to a minimum interval
	// (no-op if called too soon). Returns nil even when skipped.
	NewIP() error
	// ForceNewIP requests a new exit IP immediately, ignoring the
	// minimum interval. Used on 429 retries.
	ForceNewIP() error
	// CurrentIP returns the most recently observed exit IP, or "" if unknown.
	CurrentIP() string
}
