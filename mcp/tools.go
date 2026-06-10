package main

import (
	"context"
	"encoding/json"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"
)

// buildServer wires the daemon's verbs as MCP tools. Each tool dials the
// daemon lazily via d, so the MCP process can start before the daemon is up.
func buildServer(version string, d *daemonDialer) *server.MCPServer {
	s := server.NewMCPServer("pupptyeer", version)

	s.AddTool(
		mcp.NewTool("list_sessions",
			mcp.WithDescription("List all live PTY sessions with their metadata.")),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := d.get()
			if err != nil {
				return mcp.NewToolResultErrorf("daemon not reachable: %v", err), nil
			}
			sessions, err := c.ListSessions()
			if err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			b, _ := json.Marshal(sessions)
			return mcp.NewToolResultText(string(b)), nil
		})

	s.AddTool(
		mcp.NewTool("new_session",
			mcp.WithDescription("Spawn a command in a new PTY session; returns the session id."),
			mcp.WithString("command", mcp.Description("program to run"), mcp.Required()),
			mcp.WithArray("args", mcp.Description("command arguments")),
			mcp.WithString("cwd", mcp.Description("working directory")),
			mcp.WithInteger("cols", mcp.Description("initial columns (default 80)")),
			mcp.WithInteger("rows", mcp.Description("initial rows (default 24)"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := d.get()
			if err != nil {
				return mcp.NewToolResultErrorf("daemon not reachable: %v", err), nil
			}
			var a struct {
				Command string   `json:"command"`
				Args    []string `json:"args"`
				Cwd     string   `json:"cwd"`
				Cols    int      `json:"cols"`
				Rows    int      `json:"rows"`
			}
			if err := r.BindArguments(&a); err != nil {
				return mcp.NewToolResultErrorf("bad arguments: %v", err), nil
			}
			if a.Cols == 0 {
				a.Cols = 80
			}
			if a.Rows == 0 {
				a.Rows = 24
			}
			id, err := c.NewSession(a.Command, a.Args, a.Cwd, nil, a.Cols, a.Rows)
			if err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			return mcp.NewToolResultText(id), nil
		})

	s.AddTool(
		mcp.NewTool("send_keys",
			mcp.WithDescription(`Write text to a session's PTY input. Include a trailing \n / \r to submit a line.`),
			mcp.WithString("session", mcp.Description("session id"), mcp.Required()),
			mcp.WithString("text", mcp.Description("text to type"), mcp.Required())),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := d.get()
			if err != nil {
				return mcp.NewToolResultErrorf("daemon not reachable: %v", err), nil
			}
			session, err := r.RequireString("session")
			if err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			text, err := r.RequireString("text")
			if err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			if err := c.WritePane(session, []byte(text)); err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			return mcp.NewToolResultText("ok"), nil
		})

	s.AddTool(
		mcp.NewTool("read_screen",
			mcp.WithDescription("Return the session's current scrollback as text (ANSI escape codes included). Decoded as UTF-8; rare non-UTF-8 bytes are shown as the replacement character. For exact bytes, use the capture_pane wire verb (base64) instead."),
			mcp.WithString("session", mcp.Description("session id"), mcp.Required())),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := d.get()
			if err != nil {
				return mcp.NewToolResultErrorf("daemon not reachable: %v", err), nil
			}
			session, err := r.RequireString("session")
			if err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			data, err := c.CapturePane(session)
			if err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			return mcp.NewToolResultText(string(data)), nil
		})

	s.AddTool(
		mcp.NewTool("resize",
			mcp.WithDescription("Set this client's desired size for the session (effective size = smallest across attached clients)."),
			mcp.WithString("session", mcp.Description("session id"), mcp.Required()),
			mcp.WithInteger("cols", mcp.Description("columns"), mcp.Required()),
			mcp.WithInteger("rows", mcp.Description("rows"), mcp.Required())),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := d.get()
			if err != nil {
				return mcp.NewToolResultErrorf("daemon not reachable: %v", err), nil
			}
			session, err := r.RequireString("session")
			if err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			if err := c.Resize(session, r.GetInt("cols", 0), r.GetInt("rows", 0)); err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			return mcp.NewToolResultText("ok"), nil
		})

	s.AddTool(
		mcp.NewTool("kill",
			mcp.WithDescription("Terminate a session's PTY."),
			mcp.WithString("session", mcp.Description("session id"), mcp.Required())),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := d.get()
			if err != nil {
				return mcp.NewToolResultErrorf("daemon not reachable: %v", err), nil
			}
			session, err := r.RequireString("session")
			if err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			if err := c.Kill(session); err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			return mcp.NewToolResultText("ok"), nil
		})

	s.AddTool(
		mcp.NewTool("gc",
			mcp.WithDescription("Reap sessions idle (no PTY input or output) for at least max_idle_seconds; returns the reaped sessions' metadata as JSON. max_idle_seconds=0 reaps every session. Attaching alone does not count as activity."),
			mcp.WithInteger("max_idle_seconds", mcp.Description("minimum idle seconds before a session is reaped (0 = all)"), mcp.Required())),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := d.get()
			if err != nil {
				return mcp.NewToolResultErrorf("daemon not reachable: %v", err), nil
			}
			reaped, err := c.GC(r.GetInt("max_idle_seconds", 0))
			if err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			b, _ := json.Marshal(reaped)
			return mcp.NewToolResultText(string(b)), nil
		})

	return s
}
