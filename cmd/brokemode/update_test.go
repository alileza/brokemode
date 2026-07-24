package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestLatestTagAndSelfUpdate(t *testing.T) {
	// Fake GitHub: /releases/latest redirects to the tag page, and the
	// asset download returns a runnable "binary".
	script := "#!/bin/sh\necho brokemode version v9.9.9\n"
	mux := http.NewServeMux()
	mux.HandleFunc("/o/r/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "/o/r/releases/tag/v9.9.9", http.StatusFound)
	})
	mux.HandleFunc("/o/r/releases/download/v9.9.9/brokemode-"+runtime.GOOS+"-"+runtime.GOARCH,
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte(script))
		})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	tag, err := latestTag(srv.URL, "o/r")
	if err != nil {
		t.Fatal(err)
	}
	if tag != "v9.9.9" {
		t.Fatalf("tag=%q, want v9.9.9", tag)
	}

	target := filepath.Join(t.TempDir(), "brokemode")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := selfUpdate(target, srv.URL, "o/r", tag); err != nil {
		t.Fatal(err)
	}
	got, _ := os.ReadFile(target)
	if string(got) != script {
		t.Fatalf("target not replaced: %q", got)
	}
	if fi, _ := os.Stat(target); fi.Mode()&0o111 == 0 {
		t.Fatal("replaced binary is not executable")
	}
}

func TestLatestTagNoReleases(t *testing.T) {
	// No redirect (e.g. 404 page): must error, not return garbage.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()
	if _, err := latestTag(srv.URL, "o/r"); err == nil {
		t.Fatal("expected error when no releases exist")
	}
}

func TestSelfUpdateRejectsBrokenDownload(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/o/r/releases/download/v1.0.0/brokemode-"+runtime.GOOS+"-"+runtime.GOARCH,
		func(w http.ResponseWriter, r *http.Request) {
			_, _ = w.Write([]byte("<html>not a binary</html>"))
		})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	target := filepath.Join(t.TempDir(), "brokemode")
	if err := os.WriteFile(target, []byte("#!/bin/sh\necho old\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := selfUpdate(target, srv.URL, "o/r", "v1.0.0"); err == nil {
		t.Fatal("a non-runnable download must not replace the binary")
	}
	got, _ := os.ReadFile(target)
	if string(got) != "#!/bin/sh\necho old\n" {
		t.Fatal("original binary was clobbered by a broken download")
	}
}
