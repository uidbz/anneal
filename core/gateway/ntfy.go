package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"
)

type ntfyTransport struct {
	cfg   TransportConfig
	token string
	http  *http.Client
}

func newNtfyTransport(cfg TransportConfig) (*ntfyTransport, error) {
	if cfg.TokenEnv != "" && os.Getenv(cfg.TokenEnv) == "" {
		log.Printf("gateway: ntfy %s: %s is empty; using unauthenticated requests", cfg.Name, cfg.TokenEnv)
	}
	return &ntfyTransport{
		cfg:   cfg,
		token: os.Getenv(cfg.TokenEnv),
		http:  &http.Client{},
	}, nil
}

func (t *ntfyTransport) Name() string     { return t.cfg.Name }
func (t *ntfyTransport) HomeChat() string { return t.cfg.Topic }

func (t *ntfyTransport) Start(ctx context.Context, inbox chan<- Inbound, cursor Cursor) error {
	key := "ntfy:" + t.cfg.BaseURL + ":" + t.cfg.InTopic
	backoff := time.Second
	for ctx.Err() == nil {
		since := cursor.Cursor(key)
		if since == "" {
			since = "30m"
		}
		err := t.subscribe(ctx, since, key, inbox, cursor)
		if ctx.Err() != nil {
			return nil
		}
		if err != nil {
			log.Printf("gateway: ntfy %s: %v", t.cfg.Name, err)
		}
		if !sleepCtx(ctx, backoff) {
			return nil
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return nil
}

func (t *ntfyTransport) subscribe(ctx context.Context, since, key string, inbox chan<- Inbound, cursor Cursor) error {
	endpoint := t.cfg.BaseURL + "/" + url.PathEscape(t.cfg.InTopic) + "/json?since=" + url.QueryEscape(since)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/json")
	t.auth(req)
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound && since != "30m" {
		return t.subscribe(ctx, "30m", key, inbox, cursor)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("subscribe %s: %s: %s", endpoint, resp.Status, strings.TrimSpace(string(body)))
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 2*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue
		}
		var msg struct {
			ID      string `json:"id"`
			Event   string `json:"event"`
			Topic   string `json:"topic"`
			Message string `json:"message"`
			Time    int64  `json:"time"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			continue
		}
		if msg.Event != "message" || msg.Message == "" {
			continue
		}
		in := Inbound{
			Transport: t.cfg.Name,
			Chat:      t.cfg.Topic, // replies go to the outbound topic
			ID:        msg.ID,
			Text:      msg.Message,
			Time:      time.Unix(msg.Time, 0),
		}
		select {
		case inbox <- in:
			cursor.SetCursor(key, msg.ID)
		case <-ctx.Done():
			return nil
		}
	}
	return sc.Err()
}

func (t *ntfyTransport) Send(ctx context.Context, chat string, msg Message) error {
	topic := chat
	if topic == "" {
		topic = t.cfg.Topic
	}
	endpoint := t.cfg.BaseURL + "/" + url.PathEscape(topic)
	body := msg.Text
	if strings.TrimSpace(body) == "" {
		body = " "
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "text/plain; charset=utf-8")
	req.Header.Set("Title", msg.Title)
	if msg.Priority > 0 {
		req.Header.Set("Priority", strconv.Itoa(msg.Priority))
	}
	if len(msg.Tags) > 0 {
		req.Header.Set("Tags", strings.Join(msg.Tags, ","))
	}
	if msg.Markdown {
		req.Header.Set("Markdown", "yes")
	}
	t.auth(req)
	resp, err := t.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return fmt.Errorf("publish %s: %s: %s", endpoint, resp.Status, strings.TrimSpace(string(b)))
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	return nil
}

func (t *ntfyTransport) Close() error { return nil }

func (t *ntfyTransport) auth(req *http.Request) {
	if t.token != "" {
		req.Header.Set("Authorization", "Bearer "+t.token)
	}
}
