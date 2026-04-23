package version

// Version is injected via -ldflags at build time.
var Version = "dev"

// SHA is the short commit SHA, injected via -ldflags at build time.
var SHA = "unknown"
