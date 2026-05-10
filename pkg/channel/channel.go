// Package channel exposes the build channel (dev or release) chosen at
// link time by cmd/ccpulse/main.go via an ldflag-injected variable.
//
// Default is "dev". Release builds set "release" through main.buildChannel
// at the ldflag layer and call Set during startup. Anything other than
// "release" normalises to "dev" — there is no third channel.
//
// Set is not goroutine-safe and must be called once at startup before any
// goroutine reads Channel or IsDev.
package channel

var current = "dev"

// Set is called once during main() with the value of main.buildChannel.
// Unknown values normalise to "dev" so a typo can never silently behave
// like a release.
func Set(c string) {
	if c == "release" {
		current = "release"
		return
	}
	current = "dev"
}

// Channel returns "dev" or "release".
func Channel() string { return current }

// IsDev reports whether this build is the dev channel.
func IsDev() bool { return current == "dev" }
