// Package tieindex maintains tie-backed word-token indexes over fyagent's
// dive JSONL sessions. Conversation truth stays in the FileStore; tie is a
// rebuildable search index (collection "fyagent" by default).
//
// Schema (exact-match triples; search = reverse-association AND algebra):
//
//	(sessionID, tie-type, session)     (sessionID, title, <t>)
//	(sessionID, project, <cwd>)        (sessionID, updated-at, <RFC3339-UTC>)
//	(sessionID, model, <id>)           (sessionID, session-msg, <msgID>)
//	(msgID, tie-type, session-msg)     (msgID, session-of, <sessionID>)
//	(msgID, role, user|assistant|tool) (msgID, tool, <name>)
//	(msgID, turn, <n>)                 (msgID, msg-at, <RFC3339-UTC>)
//	(msgID, snippet, <first 400 ch>)   (msgID, tag, <lower token len>=3>)
//
// Tokens live under relation "tag" (not a private "word" relation): every
// tie server reverse-indexes tag and tie-type, which is exactly what the
// value-based Search below needs; older servers only index those two in
// reverse, so anything else would be invisible to queries.
//
// msgID = sha1(sessionID + ":" + position) hex — stable across re-index;
// Add is idempotent, so re-indexing never duplicates.
package tieindex

import (
	"context"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"strings"
	"sync"
	"time"
	"unicode"

	"fyagent/core/config"

	"github.com/uidbz/tie/client"
)

// Indexer owns the tie connection and the incremental index state.
type Indexer struct {
	cfg        config.Tie
	collection string

	mu      sync.Mutex
	tc      *client.TieClient // nil = degraded (not connected)
	indexed map[string]int    // sessionID -> messages indexed so far
	lastErr string
}

// New builds an indexer. Connection is lazy: a tie outage never blocks
// fyagent; Search/IndexSession report the degraded state instead.
func New(cfg config.Tie) *Indexer {
	return &Indexer{cfg: cfg, collection: cfg.Collection, indexed: map[string]int{}}
}

// Enabled reports whether indexing is switched on in config.
func (x *Indexer) Enabled() bool { return x.cfg.Enabled }

// Connect (re)establishes the tie client. Safe to call repeatedly.
func (x *Indexer) Connect() error {
	if !x.cfg.Enabled {
		return fmt.Errorf("tie index disabled in config")
	}
	tcfg, err := client.LoadConfig(x.cfg.ConfigName)
	if err != nil {
		tcfg = client.DefaultConfig()
	}
	tc := client.NewTieClientFor(tcfg, x.collection)
	// Liveness probe: any HTTP answer counts. A not-yet-created collection
	// makes even trivial queries fail semantically, and old servers lack
	// /-/version — only transport failure means "unreachable".
	if err := probeLive(tc.Config.TripleStoreURL); err != nil {
		x.setDegraded(fmt.Sprintf("triplestore unreachable: %v", err))
		return err
	}
	x.mu.Lock()
	x.tc = tc
	x.lastErr = ""
	x.mu.Unlock()
	return nil
}

// probeHTTP is the liveness-probe client (short timeout: a hung tie must
// degrade the index quickly, not block boot).
var probeHTTP = &http.Client{Timeout: 3 * time.Second}

// probeLive reports whether the triplestore answers HTTP at all. Any status
// code (even 404) proves the server is up.
func probeLive(base string) error {
	u := strings.TrimRight(base, "/") + "/-/live"
	resp, err := probeHTTP.Get(u)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
	return nil
}

// emptyCollErr is what tie replies (ErrNotFound) when a query matches
// nothing or the collection does not exist yet — a normal state, not an outage.
func emptyCollErr(err error) bool {
	return errors.Is(err, client.ErrNotFound)
}

func (x *Indexer) setDegraded(msg string) {
	x.mu.Lock()
	x.lastErr = msg
	x.tc = nil
	x.mu.Unlock()
}

// State describes the index for UI/health reporting.
func (x *Indexer) State() map[string]any {
	x.mu.Lock()
	defer x.mu.Unlock()
	online := x.tc != nil
	return map[string]any{
		"enabled":    x.cfg.Enabled,
		"online":     online,
		"collection": x.collection,
		"error":      x.lastErr,
		"sessions":   len(x.indexed),
	}
}

func (x *Indexer) client() (*client.TieClient, error) {
	x.mu.Lock()
	tc, lastErr := x.tc, x.lastErr
	x.mu.Unlock()
	if tc == nil {
		return nil, fmt.Errorf("tie index unavailable (%s)", lastErr)
	}
	return tc, nil
}

// SessionData is one session's indexable content (decoupled from dive types
// so the store layer feeds plain data).
type SessionData struct {
	ID      string
	Title   string
	Project string
	Model   string
	Updated time.Time
	Msgs    []MsgData
}

// MsgData is one conversation message in indexable form.
type MsgData struct {
	Position int
	Role     string // user | assistant | tool
	Tool     string // tool name for tool/assistant-call rows ("" otherwise)
	Text     string
}

// IndexSession (re-)indexes a session incrementally: messages beyond the
// count indexed so far in this process. First sight of a session indexes it
// fully — msgIDs are stable, so this only re-adds identical triples.
func (x *Indexer) IndexSession(sd SessionData) error {
	tc, err := x.client()
	if err != nil {
		return err
	}
	x.mu.Lock()
	done := x.indexed[sd.ID]
	x.mu.Unlock()
	if done > len(sd.Msgs) {
		done = 0 // session shrank (compaction/rollback): re-index fully
	}

	b := tc.NewBatch()
	now := time.Now().UTC().Format(time.RFC3339)
	b.Add(sd.ID, "tie-type", "session")
	if sd.Title != "" {
		b.Add(sd.ID, "title", sd.Title)
	}
	if sd.Project != "" {
		b.Add(sd.ID, "project", sd.Project)
	}
	if sd.Model != "" {
		b.Add(sd.ID, "model", sd.Model)
	}
	up := sd.Updated
	if up.IsZero() {
		up = time.Now()
	}
	b.Set(sd.ID, "updated-at", []string{up.UTC().Format(time.RFC3339)})

	for _, m := range sd.Msgs[done:] {
		mid := msgID(sd.ID, m.Position)
		b.Add(mid, "tie-type", "session-msg")
		b.Add(mid, "session-of", sd.ID)
		b.Add(sd.ID, "session-msg", mid)
		if m.Role != "" {
			b.Add(mid, "role", m.Role)
		}
		if m.Tool != "" {
			b.Add(mid, "tool", m.Tool)
		}
		b.Set(mid, "turn", []string{fmt.Sprintf("%06d", m.Position)})
		b.Set(mid, "msg-at", []string{now})
		b.Set(mid, "snippet", []string{snippet(m.Text, 400)})
		b.Set(mid, "tag", tokens(m.Text, 300)) // Set: re-index replaces stale tokens
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := tc.BatchContext(ctx, b); err != nil {
		x.setDegraded(fmt.Sprintf("index write failed: %v", err))
		return err
	}
	x.mu.Lock()
	x.indexed[sd.ID] = len(sd.Msgs)
	x.mu.Unlock()
	return nil
}

// IndexExperience upserts one harness improvement library entry's tokens
// (relation "tag", as required by older servers' reverse-index whitelist).
func (x *Indexer) IndexExperience(id, text string) error {
	tc, err := x.client()
	if err != nil {
		return err
	}
	b := tc.NewBatch()
	key := "harness-exp:" + id
	now := time.Now().UTC().Format(time.RFC3339)
	b.Add(key, "tie-type", "harness-exp")
	b.Set(key, "snippet", []string{snippet(text, 400)})
	b.Set(key, "updated-at", []string{now})
	b.Set(key, "tag", tokens(text, 300))
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if _, err := tc.BatchContext(ctx, b); err != nil {
		x.setDegraded(fmt.Sprintf("experience index write failed: %v", err))
		return err
	}
	return nil
}

// ExpHit is one library-entry token match.
type ExpHit struct {
	ID      string  `json:"id"`
	Snippet string  `json:"snippet"`
	Score   float64 `json:"score"`
}

// SearchExperience runs an exact-token AND query over library entries.
func (x *Indexer) SearchExperience(query string, limit int) ([]ExpHit, error) {
	tc, err := x.client()
	if err != nil {
		return nil, err
	}
	toks := tokens(query, 12)
	if len(toks) == 0 {
		return nil, fmt.Errorf("no searchable tokens in query")
	}
	if limit <= 0 || limit > 20 {
		limit = 5
	}
	spec := client.QuerySpec{
		Terms: toks, Reverse: true, Scope: "harness-exp",
		SortByValue: "updated-at", Descending: true, Expand: true,
		Limit: limit * 3, Offset: 0,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, _, err := tc.QueryContext(ctx, spec)
	if emptyCollErr(err) {
		return nil, nil
	}
	if err != nil {
		x.setDegraded(fmt.Sprintf("experience query failed: %v", err))
		return nil, err
	}
	var out []ExpHit
	for _, row := range rows {
		if len(out) >= limit {
			break
		}
		vals := row.Attributes["tag"]
		matched := 0
		for _, want := range toks {
			for _, have := range vals {
				if have == want {
					matched++
					break
				}
			}
		}
		score := float64(matched) / float64(len(toks))
		if score <= 0 {
			continue
		}
		id := strings.TrimPrefix(row.Key, "harness-exp:")
		out = append(out, ExpHit{ID: id, Snippet: first(row.Attributes["snippet"]), Score: score})
	}
	return out, nil
}

// Hit is one message match with its session context.
type Hit struct {
	SessionID string  `json:"sessionID"`
	Title     string  `json:"title"`
	MsgID     string  `json:"msgID"`
	Role      string  `json:"role"`
	Tool      string  `json:"tool"`
	Snippet   string  `json:"snippet"`
	MsgAt     string  `json:"msgAt"`
	Score     float64 `json:"score"` // matched-token ratio (0..1)
}

// Search runs an exact-token AND query over session messages.
func (x *Indexer) Search(query string, limit int) ([]Hit, error) {
	tc, err := x.client()
	if err != nil {
		return nil, err
	}
	toks := tokens(query, 12)
	if len(toks) == 0 {
		return nil, fmt.Errorf("no searchable tokens in query")
	}
	if limit <= 0 || limit > 50 {
		limit = 10
	}
	spec := client.QuerySpec{
		Terms:       toks,
		Reverse:     true, // Terms are values (tags); AND-intersected server-side
		Scope:       "session-msg",
		SortByValue: "msg-at",
		Descending:  true,
		Expand:      true,
		Limit:       limit * 3, // rows whose tags merely collide get filtered below
		Offset:      0,
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	rows, _, err := tc.QueryContext(ctx, spec)
	if emptyCollErr(err) {
		return nil, nil // collection empty/not created yet
	}
	if err != nil {
		x.setDegraded(fmt.Sprintf("query failed: %v", err))
		return nil, err
	}

	var hits []Hit
	for _, row := range rows {
		vals := row.Attributes["tag"]
		matched := 0
		for _, want := range toks {
			for _, have := range vals {
				if have == want {
					matched++
					break
				}
			}
		}
		if matched < len(toks) {
			continue // collision via another relation (tool name etc.)
		}
		sid := first(row.Attributes["session-of"])
		hits = append(hits, Hit{
			SessionID: sid,
			Title:     first(row.Attributes["title"]),
			MsgID:     row.Key,
			Role:      first(row.Attributes["role"]),
			Tool:      first(row.Attributes["tool"]),
			Snippet:   first(row.Attributes["snippet"]),
			MsgAt:     first(row.Attributes["msg-at"]),
			Score:     float64(matched) / float64(len(toks)),
		})
		if len(hits) >= limit {
			break
		}
	}
	// Fill in session titles from the session rows (msg rows don't carry them
	// unless the query expanded the session-of edge — cheap targeted expand).
	if missing := titlesMissing(hits); len(missing) > 0 {
		srows, err := tc.ExpandContext(ctx, missing)
		if err == nil {
			byID := map[string]string{}
			for _, r := range srows {
				byID[r.Key] = first(r.Attributes["title"])
			}
			for i := range hits {
				if hits[i].Title == "" {
					hits[i].Title = byID[hits[i].SessionID]
				}
			}
		}
	}
	return hits, nil
}

func titlesMissing(hits []Hit) []string {
	seen := map[string]bool{}
	var out []string
	for _, h := range hits {
		if h.SessionID != "" && h.Title == "" && !seen[h.SessionID] {
			seen[h.SessionID] = true
			out = append(out, h.SessionID)
		}
	}
	return out
}

func first(v []string) string {
	if len(v) > 0 {
		return v[0]
	}
	return ""
}

func msgID(sessID string, pos int) string {
	sum := sha1.Sum([]byte(fmt.Sprintf("%s:%d", sessID, pos)))
	return hex.EncodeToString(sum[:])[:32]
}

func snippet(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n])
}

// tokens lowercases, splits on non-alphanumerics, keeps len>=3, dedupes,
// and caps the count.
func tokens(s string, cap int) []string {
	var out []string
	seen := map[string]bool{}
	f := func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }
	for _, w := range strings.FieldsFunc(strings.ToLower(s), f) {
		if len(w) < 3 || seen[w] {
			continue
		}
		seen[w] = true
		out = append(out, w)
		if cap > 0 && len(out) >= cap {
			break
		}
	}
	return out
}

// LogState logs the index state at startup.
func (x *Indexer) LogState() {
	st := x.State()
	if st["online"] == true {
		log.Printf("tieindex: online collection=%s store=%s", x.collection, x.storeURL())
		return
	}
	log.Printf("tieindex: DEGRADED (%v) — session_search disabled until tie is reachable", st["error"])
}

func (x *Indexer) storeURL() string {
	x.mu.Lock()
	defer x.mu.Unlock()
	if x.tc == nil {
		return ""
	}
	return x.tc.Config.TripleStoreURL
}
