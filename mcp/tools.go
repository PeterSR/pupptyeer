package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/mark3labs/mcp-go/mcp"
	"github.com/mark3labs/mcp-go/server"

	client "github.com/PeterSR/pupptyeer/clients/go"
)

// cursorVis renders cursor visibility for the read_screen footer.
func cursorVis(visible bool) string {
	if visible {
		return ""
	}
	return " (hidden)"
}

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
			mcp.WithInteger("rows", mcp.Description("initial rows (default 24)")),
			mcp.WithBoolean("raw", mcp.Description("don't run a terminal emulator for this session (lower CPU/latency); read_screen rendered grid is then unavailable, raw scrollback still works. Default false.")),
			mcp.WithString("requested_id", mcp.Description("use this string as the session id instead of a daemon-generated UUID. If an alive session already holds it, this errors unless get_or_create is set.")),
			mcp.WithBoolean("get_or_create", mcp.Description("when an alive session already holds requested_id, return that existing session as-is (continuation: same id, same live program) instead of erroring. Default false."))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := d.get()
			if err != nil {
				return mcp.NewToolResultErrorf("daemon not reachable: %v", err), nil
			}
			var a struct {
				Command     string   `json:"command"`
				Args        []string `json:"args"`
				Cwd         string   `json:"cwd"`
				Cols        int      `json:"cols"`
				Rows        int      `json:"rows"`
				Raw         bool     `json:"raw"`
				RequestedID string   `json:"requested_id"`
				GetOrCreate bool     `json:"get_or_create"`
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
			var opts []client.SessionOption
			if a.Raw {
				opts = append(opts, client.WithRaw())
			}
			if a.RequestedID != "" {
				opts = append(opts, client.WithSessionID(a.RequestedID))
			}
			if a.GetOrCreate {
				opts = append(opts, client.WithGetOrCreate())
			}
			id, err := c.NewSession(a.Command, a.Args, a.Cwd, nil, a.Cols, a.Rows, opts...)
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
			mcp.WithDescription("Read the session's screen. By default returns the rendered visible grid (escape codes applied, one line per row) - the right view for inspecting a TUI. Set render=false for raw scrollback text (ANSI included). settle_ms waits for the screen to go quiet before reading (use it after sending input); timeout_ms caps that wait. This reports what is on the screen, not what it means."),
			mcp.WithString("session", mcp.Description("session id"), mcp.Required()),
			mcp.WithBoolean("render", mcp.Description("rendered visible grid (default true); false for raw scrollback text")),
			mcp.WithNumber("settle_ms", mcp.Description("wait until no output for this many ms before reading (0 = no wait)")),
			mcp.WithNumber("timeout_ms", mcp.Description("cap on the settle wait in ms; <=0 uses the daemon default"))),
		func(ctx context.Context, r mcp.CallToolRequest) (*mcp.CallToolResult, error) {
			c, err := d.get()
			if err != nil {
				return mcp.NewToolResultErrorf("daemon not reachable: %v", err), nil
			}
			session, err := r.RequireString("session")
			if err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			var opts []client.CaptureOption
			if ms := r.GetInt("settle_ms", 0); ms > 0 {
				opts = append(opts, client.WithSettle(ms))
			}
			if ms := r.GetInt("timeout_ms", 0); ms > 0 {
				opts = append(opts, client.WithTimeout(ms))
			}
			if !r.GetBool("render", true) {
				data, err := c.CapturePane(session, opts...)
				if err != nil {
					return mcp.NewToolResultErrorf("%v", err), nil
				}
				return mcp.NewToolResultText(string(data)), nil
			}
			scr, err := c.CaptureScreen(session, opts...)
			if err != nil {
				return mcp.NewToolResultErrorf("%v", err), nil
			}
			out := strings.Join(scr.Lines, "\n")
			alt := ""
			if scr.AltScreen {
				alt = " alt_screen"
			}
			out += fmt.Sprintf("\n--\n%dx%d cursor=%d,%d%s%s", scr.Cols, scr.Rows,
				scr.Cursor.Row, scr.Cursor.Col, cursorVis(scr.Cursor.Visible), alt)
			return mcp.NewToolResultText(out), nil
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
