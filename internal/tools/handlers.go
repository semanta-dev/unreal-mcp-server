package tools

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/jdziat/unreal-mcp-server/internal/bridge"
	"github.com/jdziat/unreal-mcp-server/internal/uexec"
)

// executePython is the arbitrary-code escape hatch: it does not route through
// the companion dispatch table, matching the Python server's execute_python.
func executePython(b *bridge.Bridge) mcp.ToolHandlerFor[executePythonIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in executePythonIn) (*mcp.CallToolResult, any, error) {
		b := bridgeFromCtx(ctx, b)
		if in.Evaluate {
			s, err := b.Eval(ctx, in.Code)
			if err != nil {
				return nil, nil, err
			}
			return textResult(s), nil, nil
		}
		res, err := b.RunPython(ctx, in.Code, uexec.ModeExecFile)
		if err != nil {
			return nil, nil, err
		}
		return textResult(bridge.FormatOutput(res)), nil, nil
	}
}

// executeConsoleCommand is a PROTO (raw remote-exec) tool, matching the Python
// server: it runs the command and returns FormatOutput of the full result, so
// any captured Info/Warning/Error the editor logs is surfaced (not just a canned
// message). The command is injected inject-safely via a base64 literal decoded
// inside the snippet.
func executeConsoleCommand(b *bridge.Bridge) mcp.ToolHandlerFor[consoleIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in consoleIn) (*mcp.CallToolResult, any, error) {
		b := bridgeFromCtx(ctx, b)
		b64 := base64.StdEncoding.EncodeToString([]byte(in.Command))
		snippet := fmt.Sprintf(`import unreal, base64
_cmd = base64.b64decode(%q).decode("utf-8")
unreal.SystemLibrary.execute_console_command(None, _cmd)
print("Executed console command: " + _cmd)`, b64)
		res, err := b.RunPython(ctx, snippet, uexec.ModeExecFile)
		if err != nil {
			return nil, nil, err
		}
		return textResult(bridge.FormatOutput(res)), nil, nil
	}
}

// listActors returns a bare JSON array of actors as text content, matching the
// Python server's list_actors contract (a top-level array, not an object).
func listActors(b *bridge.Bridge) mcp.ToolHandlerFor[listActorsIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in listActorsIn) (*mcp.CallToolResult, any, error) {
		b := bridgeFromCtx(ctx, b)
		raw, err := b.Call(ctx, "list_actors", map[string]any{"name_filter": in.NameFilter})
		if err != nil {
			return nil, nil, err
		}
		return textResult(string(raw)), nil, nil
	}
}

// takeScreenshot dispatches the SceneCapture2D op (which exports a PNG to
// Saved/Screenshots) then reads the bytes back as image content. A per-call
// filename avoids the fixed-name race of the Python version.
func takeScreenshot(b *bridge.Bridge) mcp.ToolHandlerFor[takeScreenshotIn, any] {
	return func(ctx context.Context, _ *mcp.CallToolRequest, in takeScreenshotIn) (*mcp.CallToolResult, any, error) {
		b := bridgeFromCtx(ctx, b)
		fname := fmt.Sprintf("mcp_%d_%d.png", os.Getpid(), time.Now().UnixNano())
		args := map[string]any{"filename": fname}
		if in.Width > 0 {
			args["width"] = in.Width
		}
		if in.Height > 0 {
			args["height"] = in.Height
		}
		if len(in.CameraLocation) > 0 {
			args["camera_location"] = in.CameraLocation
		}
		if len(in.CameraRotationPyr) > 0 {
			args["camera_rotation_pyr"] = in.CameraRotationPyr
		}
		raw, err := b.Call(ctx, "take_screenshot", args)
		if err != nil {
			return nil, nil, err
		}
		var r struct {
			File  string `json:"file"`
			Error string `json:"error"`
		}
		if err := json.Unmarshal(raw, &r); err != nil {
			return nil, nil, err
		}
		if r.Error != "" {
			return nil, nil, fmt.Errorf("screenshot: %s", r.Error)
		}
		data, err := readImageFile(r.File, 10*time.Second)
		if err != nil {
			return nil, nil, err
		}
		return &mcp.CallToolResult{
			Content: []mcp.Content{&mcp.ImageContent{Data: data, MIMEType: "image/png"}},
		}, nil, nil
	}
}

// readImageFile polls for the exported PNG (export is synchronous but the file
// write may lag), reads it, and removes it.
func readImageFile(path string, timeout time.Duration) ([]byte, error) {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fi, err := os.Stat(path); err == nil && fi.Size() > 0 {
			data, err := os.ReadFile(path)
			if err == nil {
				_ = os.Remove(path)
				return data, nil
			}
		}
		time.Sleep(200 * time.Millisecond)
	}
	return nil, fmt.Errorf("screenshot export did not produce %s", path)
}
