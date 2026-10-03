// companion.go embeds the companion in-editor Python module and exposes its
// companionSource and companionVersion. The module is kept as ordered section files (py/NN_*.py)
// that are concatenated, in filename order, into one companionSource — so the editor still
// receives a single module (identical hot-load/on-disk semantics). The Go bridge hot-loads this module into
// the editor's __main__ namespace and dispatches structured ops into it with
// base64-JSON args, so Go never hand-builds Python (GO_REWRITE_PLAN.md §8).
package bridge

import (
	"embed"
	"io/fs"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed py/[0-9]*.py
var companionFS embed.FS

// companionSource is the concatenation of the section files in filename order.
var companionSource = mustConcatCompanion(companionFS)

// CompanionFiles returns the embedded section file names in concatenation order.
func CompanionFiles() []string {
	names, err := fs.Glob(companionFS, "py/[0-9]*.py")
	if err != nil {
		panic("snippets: " + err.Error())
	}
	sort.Strings(names)
	return names
}

func mustConcatCompanion(fsys embed.FS) string {
	var sb strings.Builder
	for _, name := range CompanionFiles() {
		b, err := fsys.ReadFile(name)
		if err != nil {
			panic("snippets: read " + name + ": " + err.Error())
		}
		sb.Write(b)
	}
	return sb.String()
}

// companionVersion is parsed from the embedded module so the .py file is the single
// companionSource of truth (a drift between Go and Python would reinstall forever).
var companionVersion = mustParseVersion(companionSource)

var companionVersionRe = regexp.MustCompile(`(?m)^_MCP_BRIDGE_VERSION\s*=\s*(\d+)`)

func mustParseVersion(src string) int {
	m := companionVersionRe.FindStringSubmatch(src)
	if m == nil {
		panic("snippets: _MCP_BRIDGE_VERSION not found in py/00_prelude.py")
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		panic("snippets: bad _MCP_BRIDGE_VERSION: " + err.Error())
	}
	return n
}

// CompanionVersion is the companion module's companionVersion (the install sentinel).
func CompanionVersion() int { return companionVersion }

// CompanionSource is the full companion module Python companionSource.
func CompanionSource() string { return companionSource }

var opsBlockRe = regexp.MustCompile(`(?s)\n_OPS\s*=\s*\{(.*?)\n\}`)
var opKeyRe = regexp.MustCompile(`(?m)^\s*"([a-z_][a-z0-9_]*)"\s*:`)

// CompanionOps returns the sorted op names registered in the module's _OPS table — the
// authoritative op list the Cockpit capability manifest must classify (the §5.3 bijection
// conformance test keys off this, so a new op that lacks a classification fails CI).
func CompanionOps() []string {
	block := opsBlockRe.FindStringSubmatch(companionSource)
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
