// Command mcpcall drives an MCP server over stdio for live validation: it starts
// the server command given after "--", reads one call per line from stdin
// ({"tool": "...", "args": {...}} or {"sleep_s": N}), and prints each result as
// one JSON line. Image content is written to -images and replaced by its path.
//
//	mcpcall -images out -- dist/unreal-mcp.exe -project <dir> < calls.jsonl
package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type call struct {
	Tool   string         `json:"tool"`
	Args   map[string]any `json:"args"`
	SleepS float64        `json:"sleep_s"`
	Note   string         `json:"note"`
}

func main() {
	images := flag.String("images", "", "directory for image content (default: discard)")
	timeout := flag.Duration("timeout", 10*time.Minute, "overall deadline")
	flag.Parse()
	argv := flag.Args()
	if len(argv) == 0 {
		fmt.Fprintln(os.Stderr, "usage: mcpcall [-images dir] -- <server> [args...] < calls.jsonl")
		os.Exit(2)
	}
	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()
	cmd := exec.Command(argv[0], argv[1:]...)
	cmd.Stderr = os.Stderr
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "mcpcall", Version: "1"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "connect:", err)
		os.Exit(1)
	}
	defer cs.Close()
	out := json.NewEncoder(os.Stdout)
	sc := bufio.NewScanner(os.Stdin)
	sc.Buffer(make([]byte, 1<<20), 1<<24)
	n := 0
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 || line[0] == '#' {
			continue
		}
		var c call
		if err := json.Unmarshal(line, &c); err != nil {
			fmt.Fprintln(os.Stderr, "bad line:", err)
			os.Exit(2)
		}
		if c.SleepS > 0 {
			time.Sleep(time.Duration(c.SleepS * float64(time.Second)))
			continue
		}
		n++
		start := time.Now()
		res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: c.Tool, Arguments: c.Args})
		rec := map[string]any{"n": n, "tool": c.Tool, "args": c.Args, "note": c.Note, "ms": time.Since(start).Milliseconds()}
		if err != nil {
			rec["transport_error"] = err.Error()
			_ = out.Encode(rec)
			continue
		}
		rec["is_error"] = res.IsError
		rec["structured"] = res.StructuredContent
		var texts, imgs []string
		for i, ct := range res.Content {
			switch v := ct.(type) {
			case *mcp.TextContent:
				texts = append(texts, v.Text)
			case *mcp.ImageContent:
				desc := fmt.Sprintf("%s %d bytes", v.MIMEType, len(v.Data))
				if *images != "" {
					p := filepath.Join(*images, fmt.Sprintf("%03d_%s_%d.png", n, c.Tool, i))
					_ = os.MkdirAll(*images, 0o755)
					_ = os.WriteFile(p, v.Data, 0o644)
					desc += " -> " + p
				}
				imgs = append(imgs, desc)
			default:
				b, _ := json.Marshal(ct)
				texts = append(texts, string(b))
			}
		}
		if len(texts) > 0 && res.StructuredContent == nil {
			rec["text"] = texts
		}
		if len(imgs) > 0 {
			rec["images"] = imgs
		}
		_ = out.Encode(rec)
	}
}
