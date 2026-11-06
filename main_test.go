package main

import (
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckAddr(t *testing.T) {
	for _, ok := range []string{"127.0.0.1:8080", "[::1]:8080", "100.76.110.54:443"} {
		if _, err := checkAddr(ok); err != nil {
			t.Errorf("%s refused: %v", ok, err)
		}
	}
	for _, bad := range []string{
		":8080", "0.0.0.0:8080", "[::]:8080", "[::ffff:0.0.0.0]:8080",
		"[fe80::1%eth0]:8080", "[::%lo]:8080", "localhost:8080", "127.0.0.1", "127.0.0.1:0", "*:8080", "",
	} {
		if _, err := checkAddr(bad); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestListenNeedsExactlyOne(t *testing.T) {
	if _, err := listen("", ""); err == nil {
		t.Error("neither: no error")
	}
	if _, err := listen(filepath.Join(t.TempDir(), "s"), "127.0.0.1:1"); err == nil {
		t.Error("both: no error")
	}
}

// socketDir is a short path: a unix socket's path is limited to about 100
// bytes, and t.TempDir can be longer than that.
func socketDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "sw")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func TestStaleSocketIsReplaced(t *testing.T) {
	path := filepath.Join(socketDir(t), "s")
	// A socket file nobody listens on, as a hibernated app leaves behind.
	l, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	l.(*net.UnixListener).SetUnlinkOnClose(false)
	l.Close()
	if _, err := os.Lstat(path); err != nil {
		t.Fatal("stale socket not left behind:", err)
	}

	l2, err := listen(path, "")
	if err != nil {
		t.Fatalf("stale socket: %v", err)
	}
	defer l2.Close()

	// Now it is live, and a second server must not take it.
	if _, err := listen(path, ""); err == nil || !strings.Contains(err.Error(), "in use") {
		t.Errorf("live socket: err = %v, want in use", err)
	}
}

func TestNonSocketIsLeftAlone(t *testing.T) {
	path := filepath.Join(socketDir(t), "s")
	if err := os.WriteFile(path, []byte("not a socket"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := listen(path, ""); err == nil {
		t.Fatal("listened over a regular file")
	}
	if b, err := os.ReadFile(path); err != nil || string(b) != "not a socket" {
		t.Errorf("file was touched: %q, %v", b, err)
	}

	// A symlink to a socket is not a socket either.
	link := filepath.Join(filepath.Dir(path), "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := listen(link, ""); err == nil {
		t.Error("listened over a symlink")
	}
}
