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

require (
	github.com/charmbracelet/x/vt v0.0.0-20260608090822-c3ad58c6c9e5
	golang.org/x/sys v0.46.0
)

require (
	github.com/charmbracelet/colorprofile v0.4.2 // indirect
	github.com/charmbracelet/ultraviolet v0.0.0-20260303162955-0b88c25f3fff // indirect
	github.com/charmbracelet/x/ansi v0.11.7 // indirect
	github.com/charmbracelet/x/exp/ordered v0.1.0 // indirect
	github.com/charmbracelet/x/term v0.2.2 // indirect
	github.com/charmbracelet/x/termios v0.1.1 // indirect
	github.com/charmbracelet/x/windows v0.2.2 // indirect
	github.com/clipperhouse/displaywidth v0.11.0 // indirect
	github.com/clipperhouse/uax29/v2 v2.7.0 // indirect
	github.com/lucasb-eyer/go-colorful v1.4.0 // indirect
	github.com/mattn/go-runewidth v0.0.23 // indirect
	github.com/muesli/cancelreader v0.2.2 // indirect
	github.com/rivo/uniseg v0.4.7 // indirect
	github.com/xo/terminfo v0.0.0-20220910002029-abceb7e1c41e // indirect
	golang.org/x/sync v0.19.0 // indirect
)
