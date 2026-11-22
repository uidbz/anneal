//go:build cgo && goolm

package gateway

import (
	"os"
	"path/filepath"
	"testing"
)

func TestMatrixE2EEConstructorRequiresSecrets(t *testing.T) {
	t.Setenv("TEST_E2EE_TOKEN", "")
	t.Setenv("TEST_E2EE_PICKLE", "")
	if _, err := newMatrixE2EETransport(TransportConfig{
		Name: "matrix", Type: "matrix", Homeserver: "https://m", UserID: "@a:m",
		RoomID: "!r:m", AllowUsers: []string{"@u:m"}, TokenEnv: "TEST_E2EE_TOKEN",
		PickleKey: "TEST_E2EE_PICKLE",
	}); err == nil {
		t.Fatal("missing token accepted")
	}
	t.Setenv("TEST_E2EE_TOKEN", "tok")
	if _, err := newMatrixE2EETransport(TransportConfig{
		Name: "matrix", Type: "matrix", Homeserver: "https://m", UserID: "@a:m",
		RoomID: "!r:m", AllowUsers: []string{"@u:m"}, TokenEnv: "TEST_E2EE_TOKEN",
		PickleKey: "TEST_E2EE_PICKLE",
	}); err == nil {
		t.Fatal("missing pickle key accepted")
	}
	t.Setenv("TEST_E2EE_PICKLE", "a-stable-long-secret")
	db := filepath.Join(t.TempDir(), "matrix", "test.db")
	tr, err := newMatrixE2EETransport(TransportConfig{
		Name: "matrix", Type: "matrix", Homeserver: "https://m", UserID: "@a:m",
		RoomID: "!r:m", AllowUsers: []string{"@u:m"}, TokenEnv: "TEST_E2EE_TOKEN",
		PickleKey: "TEST_E2EE_PICKLE", DBPath: db,
	})
	if err != nil {
		t.Fatal(err)
	}
	if tr.Name() != "matrix" || tr.HomeChat() != "!r:m" {
		t.Fatalf("transport: %s %s", tr.Name(), tr.HomeChat())
	}
	if fi, err := os.Stat(filepath.Dir(db)); err != nil || !fi.IsDir() {
		t.Fatalf("db directory not created: %v", err)
	}
}
