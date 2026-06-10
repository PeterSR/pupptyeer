// pupptyeer-mcp is its own module so the MCP/HTTP/OAuth dependency surface
// (mcp-go, go-oidc, …) never enters the daemon's build graph. It depends only
// on the zero-dep Go client and talks to the daemon over the unix socket.
module github.com/PeterSR/pupptyeer/mcp

go 1.25.10

replace github.com/PeterSR/pupptyeer/clients/go => ../clients/go

require (
	github.com/PeterSR/pupptyeer/clients/go v0.0.0-00010101000000-000000000000
	github.com/coreos/go-oidc/v3 v3.18.0
	github.com/mark3labs/mcp-go v0.54.0
)

require (
	github.com/go-jose/go-jose/v4 v4.1.4 // indirect
	github.com/google/jsonschema-go v0.4.2 // indirect
	github.com/google/uuid v1.6.0 // indirect
	github.com/santhosh-tekuri/jsonschema/v6 v6.0.2 // indirect
	github.com/spf13/cast v1.7.1 // indirect
	github.com/yosida95/uritemplate/v3 v3.0.2 // indirect
	golang.org/x/oauth2 v0.36.0 // indirect
	golang.org/x/text v0.14.0 // indirect
)
