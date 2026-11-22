//go:build cgo && goolm

package gateway

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sync"
	"time"

	"maunium.net/go/mautrix"
	"maunium.net/go/mautrix/crypto/cryptohelper"
	"maunium.net/go/mautrix/event"
	"maunium.net/go/mautrix/id"
)

// matrixE2EETransport is the encrypted Matrix transport. It uses mautrix-go's
// OlmMachine + Megolm and an embedded SQLite crypto store. The plain transport
// (matrix.go) remains dependency-free; this file is built only with CGO.
type matrixE2EETransport struct {
	cfg       TransportConfig
	token     string
	pickleKey []byte

	mu     sync.RWMutex
	cli    *mautrix.Client
	helper *cryptohelper.CryptoHelper
	closed sync.Once
}

func newMatrixE2EETransport(cfg TransportConfig) (Transport, error) {
	token := os.Getenv(cfg.TokenEnv)
	if token == "" {
		return nil, fmt.Errorf("gateway: matrix %s: %s is empty", cfg.Name, cfg.TokenEnv)
	}
	secret := os.Getenv(cfg.PickleKey)
	if secret == "" {
		return nil, fmt.Errorf("gateway: matrix %s: %s is empty (crypto store pickle key)", cfg.Name, cfg.PickleKey)
	}
	if cfg.DBPath == "" {
		cfg.DBPath = filepath.Join(os.Getenv("ANNEAL_HOME"), "matrix", cfg.Name+".db")
	}
	if err := os.MkdirAll(filepath.Dir(cfg.DBPath), 0o700); err != nil {
		return nil, err
	}
	_ = os.Chmod(filepath.Dir(cfg.DBPath), 0o700)
	sum := sha256.Sum256([]byte(secret))
	return &matrixE2EETransport{cfg: cfg, token: token, pickleKey: sum[:]}, nil
}

func (t *matrixE2EETransport) Name() string     { return t.cfg.Name }
func (t *matrixE2EETransport) HomeChat() string { return t.cfg.RoomID }

func (t *matrixE2EETransport) Start(ctx context.Context, inbox chan<- Inbound, _ Cursor) error {
	backoff := time.Second
	for ctx.Err() == nil {
		err := t.run(ctx, inbox)
		if ctx.Err() != nil {
			return nil
		}
		log.Printf("gateway: matrix-e2ee %s: %v", t.cfg.Name, err)
		if !sleepCtx(ctx, backoff) {
			return nil
		}
		if backoff < 30*time.Second {
			backoff *= 2
		}
	}
	return nil
}

func (t *matrixE2EETransport) run(ctx context.Context, inbox chan<- Inbound) error {
	cli, err := mautrix.NewClient(t.cfg.Homeserver, id.UserID(t.cfg.UserID), t.token)
	if err != nil {
		return err
	}
	if t.cfg.DeviceID != "" {
		cli.DeviceID = id.DeviceID(t.cfg.DeviceID)
	}
	if cli.DeviceID == "" {
		who, err := cli.Whoami(ctx)
		if err != nil {
			return fmt.Errorf("whoami: %w", err)
		}
		cli.DeviceID = who.DeviceID
		if cli.UserID == "" {
			cli.UserID = who.UserID
		}
	}
	helper, err := cryptohelper.NewCryptoHelper(cli, t.pickleKey, t.cfg.DBPath)
	if err != nil {
		return err
	}
	if err := helper.Init(ctx); err != nil {
		_ = helper.Close()
		return fmt.Errorf("crypto init: %w", err)
	}
	syncer, ok := cli.Syncer.(mautrix.ExtensibleSyncer)
	if !ok {
		_ = helper.Close()
		return fmt.Errorf("matrix syncer is not extensible")
	}
	syncer.OnEventType(event.EventMessage, func(ctx context.Context, evt *event.Event) {
		t.onMessage(ctx, inbox, evt)
	})

	t.mu.Lock()
	t.cli, t.helper = cli, helper
	t.mu.Unlock()
	defer func() {
		t.mu.Lock()
		if t.cli == cli {
			t.cli = nil
		}
		if t.helper == helper {
			t.helper = nil
		}
		t.mu.Unlock()
		cli.StopSync()
		_ = helper.Close()
	}()
	log.Printf("gateway: matrix-e2ee %s: connected as %s device %s", t.cfg.Name, cli.UserID, cli.DeviceID)
	return cli.SyncWithContext(ctx)
}

func (t *matrixE2EETransport) onMessage(ctx context.Context, inbox chan<- Inbound, evt *event.Event) {
	if evt == nil || evt.RoomID.String() != t.cfg.RoomID {
		return
	}
	if evt.Sender.String() == t.cfg.UserID {
		return
	}
	allowed := false
	for _, u := range t.cfg.AllowUsers {
		if evt.Sender.String() == u {
			allowed = true
			break
		}
	}
	if !allowed {
		return
	}
	content := evt.Content.AsMessage()
	if content.MsgType != event.MsgText && content.MsgType != event.MsgNotice {
		return
	}
	if content.Body == "" {
		return
	}
	in := Inbound{
		Transport: t.cfg.Name,
		Chat:      t.cfg.RoomID,
		ID:        evt.ID.String(),
		Text:      content.Body,
		Time:      time.UnixMilli(evt.Timestamp),
	}
	select {
	case inbox <- in:
	case <-ctx.Done():
	}
}

func (t *matrixE2EETransport) Send(ctx context.Context, chat string, msg Message) error {
	room := chat
	if room == "" {
		room = t.cfg.RoomID
	}
	t.mu.RLock()
	cli, helper := t.cli, t.helper
	t.mu.RUnlock()
	if cli == nil || helper == nil {
		return fmt.Errorf("matrix-e2ee %s is not connected", t.cfg.Name)
	}
	body := msg.Text
	if msg.Title != "" {
		body = msg.Title + "\n" + body
	}
	body = trimText(body, 12000)
	content := &event.MessageEventContent{MsgType: event.MsgText, Body: body}
	encrypted, err := helper.Encrypt(ctx, id.RoomID(room), event.EventMessage, content)
	if err != nil {
		return fmt.Errorf("encrypt: %w", err)
	}
	_, err = cli.SendMessageEvent(ctx, id.RoomID(room), event.EventEncrypted, encrypted)
	return err
}

func (t *matrixE2EETransport) Close() error {
	t.closed.Do(func() {
		t.mu.Lock()
		cli, helper := t.cli, t.helper
		t.cli, t.helper = nil, nil
		t.mu.Unlock()
		if cli != nil {
			cli.StopSync()
		}
		if helper != nil {
			_ = helper.Close()
		}
	})
	return nil
}
