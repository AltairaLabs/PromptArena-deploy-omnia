package omnia

// Build-time variables injected via ldflags.
var (
	// Version is the semantic version of this build.
	Version = "dev"
	// Commit is the git commit hash of this build.
	Commit = "none"
	// Date is the build timestamp.
	Date = "unknown"
)
