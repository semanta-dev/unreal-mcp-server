package tools

import (
	"bufio"
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jdziat/unreal-mcp-server/internal/tools/envelope"
	"github.com/jdziat/unreal-mcp-server/internal/tools/spec"
)

// project_map op=source: read the project's text files (C++, Build.cs, config, scenarios)
// without the python tool. Read-only, confined to the project directory; a directory walk
// skips build output and Content (binary assets: asset_query reads those).

const (
	sourceReadLines = 400
	sourceListMax   = 500
	sourceHitsMax   = 60 // a search is for finding: truncated says narrow it
	sourceFileMax   = 4 << 20
)

var sourceSkipDirs = map[string]bool{"binaries": true, "intermediate": true, "saved": true, "deriveddatacache": true,
	"content": true, ".git": true, ".vs": true, ".idea": true}

func projectSource(c *spec.Call, rel, match string, line int) (*spec.Result, error) {
	dir, err := projectDir(c)
	if err != nil {
		return nil, err
	}
	dir, full, err := sourcePath(dir, rel)
	if err != nil {
		return nil, err
	}
	st, err := os.Stat(full)
	if err != nil {
		return nil, envelope.New(envelope.NotFound, "%s: not found in the project", rel).
			WithHint("omit path to list the project, or pass match to search it")
	}
	shown := filepath.ToSlash(strings.TrimPrefix(strings.TrimPrefix(full, dir), string(filepath.Separator)))
	if !st.IsDir() {
		return sourceRead(full, shown, match, line)
	}
	var files []string
	hits := []map[string]any{}
	truncated := false
	walkErr := filepath.WalkDir(full, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != full && sourceSkipDirs[strings.ToLower(d.Name())] {
				return filepath.SkipDir
			}
			return nil
		}
		r := filepath.ToSlash(strings.TrimPrefix(strings.TrimPrefix(p, dir), string(filepath.Separator)))
		if match == "" {
			if len(files) >= sourceListMax {
				truncated = true
				return filepath.SkipAll
			}
			files = append(files, r)
			return nil
		}
		for _, h := range sourceGrep(p, match, sourceHitsMax-len(hits)) {
			h["file"] = r
			hits = append(hits, h)
		}
		if len(hits) >= sourceHitsMax {
			truncated = true
			return filepath.SkipAll
		}
		return nil
	})
	if walkErr != nil {
		return nil, envelope.New(envelope.OperationFailed, "walk %s: %v", shown, walkErr)
	}
	if match != "" {
		return &spec.Result{Data: map[string]any{"path": shown, "match": match, "hits": hits, "truncated": truncated},
			Summary: fmt.Sprintf("%d line(s) matching %q under %s", len(hits), match, shown)}, nil
	}
	sort.Strings(files)
	return &spec.Result{Data: map[string]any{"path": shown, "files": files, "truncated": truncated},
		Summary: fmt.Sprintf("%d file(s) under %s", len(files), shown)}, nil
}

// sourcePath resolves a project-relative path, refusing anything outside the project
// (.., absolute paths elsewhere, or a link that leads out).
func sourcePath(dir, rel string) (root, full string, err error) {
	if root, err = filepath.EvalSymlinks(dir); err != nil {
		root = dir
	}
	p := filepath.Join(root, filepath.FromSlash(rel))
	if filepath.IsAbs(rel) {
		p = filepath.Clean(rel)
	}
	if real, err := filepath.EvalSymlinks(p); err == nil {
		p = real
	}
	if r, err := filepath.Rel(root, p); err != nil || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", "", envelope.New(envelope.InvalidArgument, "path %q is outside the project", rel)
	}
	return root, p, nil
}

func sourceRead(full, shown, match string, line int) (*spec.Result, error) {
	b, err := readText(full)
	if err != nil {
		return nil, envelope.New(envelope.InvalidArgument, "%s: %v", shown, err)
	}
	if match != "" {
		hits := sourceGrepBytes(b, match, sourceHitsMax)
		return &spec.Result{Data: map[string]any{"path": shown, "match": match, "hits": hits, "truncated": len(hits) >= sourceHitsMax},
			Summary: fmt.Sprintf("%d line(s) matching %q in %s", len(hits), match, shown)}, nil
	}
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	if line < 1 {
		line = 1
	}
	if line > len(lines) {
		return nil, envelope.New(envelope.InvalidArgument, "%s has %d lines (line %d)", shown, len(lines), line)
	}
	end := min(line-1+sourceReadLines, len(lines))
	return &spec.Result{Data: map[string]any{"path": shown, "line": line, "end_line": end, "total_lines": len(lines),
		"truncated": end < len(lines), "text": strings.Join(lines[line-1:end], "\n")},
		Summary: fmt.Sprintf("%s lines %d-%d of %d", shown, line, end, len(lines))}, nil
}

// readText reads a text file; a binary (NUL byte) or oversized file is refused.
func readText(p string) ([]byte, error) {
	st, err := os.Stat(p)
	if err != nil {
		return nil, err
	}
	if st.Size() > sourceFileMax {
		return nil, fmt.Errorf("%d bytes is over the %d-byte limit", st.Size(), sourceFileMax)
	}
	b, err := os.ReadFile(p)
	if err != nil {
		return nil, err
	}
	if bytes.IndexByte(b, 0) >= 0 {
		return nil, fmt.Errorf("binary file (assets: asset_query)")
	}
	return bytes.TrimPrefix(b, []byte("\xef\xbb\xbf")), nil
}

func sourceGrep(p, match string, max int) []map[string]any {
	b, err := readText(p)
	if err != nil {
		return nil
	}
	return sourceGrepBytes(b, match, max)
}

func sourceGrepBytes(b []byte, match string, max int) []map[string]any {
	needle := strings.ToLower(match)
	out := []map[string]any{}
	sc := bufio.NewScanner(bytes.NewReader(b))
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	for n := 1; sc.Scan() && len(out) < max; n++ {
		if t := sc.Text(); strings.Contains(strings.ToLower(t), needle) {
			out = append(out, map[string]any{"line": n, "text": truncateLine(strings.TrimRight(t, "\r"), 300)})
		}
	}
	return out
}

func truncateLine(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
