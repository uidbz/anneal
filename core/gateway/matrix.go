package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// matrixTransport is a deliberately small Matrix client: one room, one bot
// token, plain text messages. It does not implement E2EE. Use a private,
// unencrypted room on your own homeserver, or a room where the bot is the only
// member besides the allowlisted humans.
type matrixTransport struct {
	cfg   TransportConfig
	token string
	http  *http.Client
}

func newMatrixTransport(cfg TransportConfig) (*matrixTransport, error) {
	token := os.Getenv(cfg.TokenEnv)
	if token == "" {
		return nil, fmt.Errorf("gateway: matrix %s: %s is empty", cfg.Name, cfg.TokenEnv)
	}
	return &matrixTransport{cfg: cfg, token: token, http: &http.Client{Timeout: 60 * time.Second}}, nil
}

func (t *matrixTransport) Name() string     { return t.cfg.Name }
func (t *matrixTransport) HomeChat() string { return t.cfg.RoomID }

func (t *matrixTransport) Start(ctx context.Context, inbox chan<- Inbound, cursor Cursor) error {
	key := "matrix:" + t.cfg.Homeserver + ":" + t.cfg.RoomID
	allowed := map[string]bool{}
	for _, u := range t.cfg.AllowUsers {
		allowed[u] = true
	}
	backoff := time.Second
	for ctx.Err() == nil {
		since := cursor.Cursor(key)
		initial := since == ""
		next, events, err := t.sync(ctx, since)
		if err != nil {
			log.Printf("gateway: matrix %s: sync: %v", t.cfg.Name, err)
			// A stale since token after a homeserver reset makes every sync
			// fail. Recover with an initial sync; do not replay history.
			if strings.Contains(err.Error(), "M_UNKNOWN_POS") {
				cursor.SetCursor(key, "")
			}
			if !sleepCtx(ctx, backoff) {
				return nil
			}
			if backoff < 30*time.Second {
				backoff *= 2
			}
			continue
		}
		backoff = time.Second
		if initial {
			cursor.SetCursor(key, next)
			continue
		}
		for _, ev := range events {
			if ev.Sender == t.cfg.UserID || !allowed[ev.Sender] {
				continue
			}
			if ev.RoomID != t.cfg.RoomID {
				continue
			}
			in := Inbound{
				Transport: t.cfg.Name,
				Chat:      t.cfg.RoomID,
				ID:        ev.EventID,
				Text:      ev.Content.Body,
				Time:      time.UnixMilli(ev.TS),
			}
			select {
			case inbox <- in:
				cursor.SetCursor(key, next)
			case <-ctx.Done():
				return nil
			}
		}
		cursor.SetCursor(key, next)
	}
	return nil
}

type matrixEvent struct {
	Type    string `json:"type"`
	Sender  string `json:"sender"`
	RoomID  string `json:"room_id"`
	EventID string `json:"event_id"`
	TS      int64  `json:"origin_server_ts"`
	Content struct {
		MsgType string `json:"msgtype"`
		Body    string `json:"body"`
	} `json:"content"`
}

// sync performs one long-poll. It returns the next_batch token and text
// messages from the configured room.
func (t *matrixTransport) sync(ctx context.Context, since string) (string, []matrixEvent, error) {
	endpoint := t.cfg.Homeserver + "/_matrix/client/v3/sync?timeout=30000"
	if since != "" {
		endpoint += "&since=" + url.QueryEscape(since)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return "", nil, err
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	resp, err := t.http.Do(req)
	if err != nil {
		return "", nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return "", nil, fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	var doc struct {
		NextBatch string `json:"next_batch"`
		Rooms     struct {
			Join map[string]struct {
				Timeline struct {
					Events []matrixEvent `json:"events"`
				} `json:"timeline"`
			} `json:"join"`
		} `json:"rooms"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&doc); err != nil {
		return "", nil, err
	}
	var out []matrixEvent
	if room, ok := doc.Rooms.Join[t.cfg.RoomID]; ok {
		for _, ev := range room.Timeline.Events {
			if ev.Type == "m.room.message" && ev.Content.MsgType == "m.text" {
				ev.RoomID = t.cfg.RoomID
				out = append(out, ev)
			}
		}
	}
	return doc.NextBatch, out, nil
}

func (t *matrixTransport) Send(ctx context.Context, chat string, msg Message) error {
	room := chat
	if room == "" {
		room = t.cfg.RoomID
	}
	body := msg.Text
	if strings.TrimSpace(body) == "" {
		body = " "
	}
	if msg.Title != "" {
		body = msg.Title + "\n" + body
	}
	body = trimText(body, 12000)
	payload, _ := json.Marshal(map[string]any{
		"msgtype": "m.text",
		"body":    body,
	})
	txn := fmt.Sprintf("anneal-%d", time.Now().UnixNano())
	endpoint := t.cfg.Homeserver + "/_matrix/client/v3/rooms/" + url.PathEscape(room) +
		"/send/m.room.message/" + url.PathEscape(txn)
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, endpoint, bytes.NewReader(payload))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+t.token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return fmt.Errorf("matrix send: %s: %s", resp.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

func (t *matrixTransport) Close() error { return nil }
