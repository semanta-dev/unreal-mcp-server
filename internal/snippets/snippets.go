// Package snippets embeds the companion in-editor Python module and exposes its
// source and version. The module is kept as ordered section files (py/NN_*.py)
// that are concatenated, in filename order, into one source — so the editor still
// receives a single module (identical hot-load/on-disk semantics). The Go bridge hot-loads this module into
// the editor's __main__ namespace and dispatches structured ops into it with
// base64-JSON args, so Go never hand-builds Python (GO_REWRITE_PLAN.md §8).
package snippets

import (
	"embed"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed py/[0-9]*.py
var pyFS embed.FS

// source is the concatenation of the section files in filename order.
var source = mustConcat(pyFS)

// SectionFiles returns the embedded section file names in concatenation order.
func SectionFiles() []string {
	names, err := fs.Glob(pyFS, "py/[0-9]*.py")
	if err != nil {
		panic("snippets: " + err.Error())
	}
	sort.Strings(names)
	return names
}

func mustConcat(fsys embed.FS) string {
	var sb strings.Builder
	for _, name := range SectionFiles() {
		b, err := fsys.ReadFile(name)
		if err != nil {
			panic("snippets: read " + name + ": " + err.Error())
		}
		sb.Write(b)
	}
	return sb.String()
}

// version is parsed from the embedded module so the .py file is the single
// source of truth (a drift between Go and Python would reinstall forever).
var version = mustParseVersion(source)

var versionRe = regexp.MustCompile(`(?m)^_MCP_BRIDGE_VERSION\s*=\s*(\d+)`)

func mustParseVersion(src string) int {
	m := versionRe.FindStringSubmatch(src)
	if m == nil {
		panic("snippets: _MCP_BRIDGE_VERSION not found in py/00_prelude.py")
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		panic("snippets: bad _MCP_BRIDGE_VERSION: " + err.Error())
	}
	return n
}

// Version is the companion module's version (the install sentinel).
func Version() int { return version }

// Source is the full companion module Python source.
func Source() string { return source }

var opsBlockRe = regexp.MustCompile(`(?s)\n_OPS\s*=\s*\{(.*?)\n\}`)
var opKeyRe = regexp.MustCompile(`(?m)^\s*"([a-z_][a-z0-9_]*)"\s*:`)

// OpNames returns the sorted op names registered in the module's _OPS table — the
// authoritative op list the Cockpit capability manifest must classify (the §5.3 bijection
// conformance test keys off this, so a new op that lacks a classification fails CI).
func OpNames() []string {
	block := opsBlockRe.FindStringSubmatch(source)
	if block == nil {
		panic("snippets: _OPS block not found in py/99_dispatch.py")
	}
	var names []string
	for _, m := range opKeyRe.FindAllStringSubmatch(block[1], -1) {
		names = append(names, m[1])
	}
	sort.Strings(names)
	return names
}
