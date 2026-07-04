package daemon

import "github.com/jdziat/unreal-mcp-server/internal/editorpool"

// Reattach reconciliation (MULTI_PROJECT_SYSTEM.md §6). On daemon restart every
// agent HTTP session is gone, so persisted leases can't be re-held; the goal is
// strictly: adopt every re-adoptable warm editor, kill every DAEMON-OWNED orphan
// (so none outlives the daemon), and NEVER touch a foreign editor (another daemon's
// or a human's manually-opened one on the shared multicast group). Reconciliation
// runs against GROUND TRUTH — the process table ∪ live discovery — not just the
// persisted file, so an editor spawned in the Launch→persist window (still cold-
// starting, not yet on discovery, caught by process enumeration via its
// -MCPInstanceToken) is not leaked.
//
// This is the pure decision function; the daemon supplies the two ground-truth sets
// and executes the returned Actions (kill-before-remove, re-register as warm Idle).

// Record is a persisted instance record or write-ahead spawn intent.
type Record struct {
	ID       string
	Project  string
	Token    string
	State    editorpool.State // intent-only == Starting; full record carries the real state
	PID      int
	Identity string
}

// reAdoptable: a record whose editor should be re-adopted as a warm Idle rather
// than killed — only a stable, accepting-ready state ({Idle, Leased}).
func (r Record) reAdoptable() bool { return r.State == editorpool.Idle || r.State == editorpool.Leased }

// LiveEditor is an editor observed at reattach — via the process table (Token read
// from its command line, PID from enumeration) or via live discovery (Token
// established live over its command channel; PID resolved live).
type LiveEditor struct {
	Token   string // "" when no -MCPInstanceToken / unconfirmable => FOREIGN
	PID     int
	Project string
}

// ActionKind is what to do with a record/editor during reattach.
type ActionKind string

const (
	// Adopt: re-register a re-adoptable live editor as a warm Idle instance.
	Adopt ActionKind = "adopt"
	// Kill: a daemon-owned (MY-token) orphan that isn't re-adoptable -> kill+remove.
	Kill ActionKind = "kill"
	// RemoveStale: a persisted record/intent whose editor is absent everywhere.
	RemoveStale ActionKind = "remove_stale"
)

// Action is one reconciliation decision.
type Action struct {
	Kind    ActionKind
	Record  *Record     // set for Adopt / RemoveStale
	Editor  *LiveEditor // set for Adopt / Kill
	Project string
	PID     int
	Token   string
}

// Reconcile decides reattach actions. myTokens is the set this daemon issued
// (from its persisted records+intents) — the ONLY tokens it may kill. records are
// this daemon's persisted records/intents; live is the process-table ∪ discovery
// ground truth. A foreign live editor (token not in myTokens, or "") is never in
// the output — it is left untouched.
func Reconcile(records []Record, live []LiveEditor) []Action {
	myTokens := map[string]bool{}
	recByToken := map[string]Record{}
	for _, r := range records {
		if r.Token != "" {
			myTokens[r.Token] = true
			recByToken[r.Token] = r
		}
	}

	seenToken := map[string]bool{}
	var out []Action

	for _, e := range live {
		// FOREIGN: no token, or a token this daemon didn't issue -> never touch.
		if e.Token == "" || !myTokens[e.Token] {
			continue
		}
		// Dedup: the input is process-table ∪ discovery, and a re-adoptable warm
		// editor appears in BOTH (a live process bearing the token AND answering
		// pongs) — so a token seen twice must produce ONE action, not two.
		if seenToken[e.Token] {
			continue
		}
		seenToken[e.Token] = true
		rec := recByToken[e.Token]
		if rec.reAdoptable() {
			r := rec
			ed := e
			out = append(out, Action{Kind: Adopt, Record: &r, Editor: &ed, Project: firstNonEmpty(rec.Project, e.Project), PID: e.PID, Token: e.Token})
		} else {
			// MY-token editor in an expected-dead / intent-only state -> kill+remove.
			r := rec
			ed := e
			out = append(out, Action{Kind: Kill, Record: &r, Editor: &ed, Project: firstNonEmpty(rec.Project, e.Project), PID: e.PID, Token: e.Token})
		}
	}

	// Persisted records/intents whose editor is absent everywhere -> stale -> Remove.
	for _, r := range records {
		if r.Token != "" && seenToken[r.Token] {
			continue
		}
		rec := r
		out = append(out, Action{Kind: RemoveStale, Record: &rec, Project: r.Project, PID: r.PID, Token: r.Token})
	}
	return out
}

func firstNonEmpty(a, b string) string {
	if a != "" {
		return a
	}
	return b
}
