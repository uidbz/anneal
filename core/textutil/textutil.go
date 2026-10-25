// Package textutil holds small text helpers shared by anneal packages.
package textutil

// Clip truncates s to at most n bytes and appends an ellipsis. n <= 0 means
// "no cap" (callers that need a mandatory limit pass a positive value).
func Clip(s string, n int) string {
	if n <= 0 || len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
