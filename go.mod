module github.com/PeterSR/pupptyeer

go 1.25.10

require (
	github.com/BurntSushi/toml v1.6.0
	github.com/PeterSR/pupptyeer/clients/go v0.0.0
	github.com/charmbracelet/x/conpty v0.2.0
	github.com/creack/pty v1.1.24
	github.com/google/uuid v1.6.0
	golang.org/x/term v0.44.0
)

// The Go client is a sibling module in this repo; build against the local
// copy. (It is dependency-free, so this does not affect the daemon's deps.)
replace github.com/PeterSR/pupptyeer/clients/go => ./clients/go

require golang.org/x/sys v0.46.0
