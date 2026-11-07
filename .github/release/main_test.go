// Copyright 2026 Hayabusa Cloud Co., Ltd.
// Use of this source code is governed by a BSD-style license that can be found in the LICENSE file.

package main_test

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"sync"
	"testing"
)

var driver string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "atomix-release-test-")
	if err != nil {
		panic(err)
	}
	driver = filepath.Join(dir, "release")
	cmd := exec.Command("go", "build", "-o", driver, ".")
	cmd.Stdout, cmd.Stderr = os.Stdout, os.Stderr
	if err := cmd.Run(); err != nil {
		os.RemoveAll(dir)
		panic(err)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func fixture(t *testing.T) string {
	t.Helper()
	root := t.TempDir()
	write(t, filepath.Join(root, ".github/release/recipe.json"), []byte(`{"base":"base","bootstrap":"1.26.10"}`))
	write(t, filepath.Join(root, ".github/workflows/release.yml"), []byte("name: fixture\n"))
	return root
}

func write(t *testing.T, path string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, b, 0644); err != nil {
		t.Fatal(err)
	}
}

func writeJSON(t *testing.T, path string, v any) {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	write(t, path, b)
}

func cli(root, endpoint, action string, args ...string) ([]byte, error) {
	flags := []string{action, "-root", root, "-repo", "test/go", "-api", endpoint, "-graphql", endpoint + "/graphql"}
	cmd := exec.Command(driver, append(flags, args...)...)
	cmd.Env = append(os.Environ(), "GH_TOKEN=fixture-token")
	return cmd.CombinedOutput()
}

func TestDiscoveryRolloverAndFailures(t *testing.T) {
	root := fixture(t)
	for _, tc := range []struct {
		name, feed string
		status     int
		want       []string
		fail       bool
	}{
		{"rollover", `[{"version":"go1.29rc1","stable":false},{"version":"go1.29.0","stable":true},{"version":"go1.28.9","stable":true},{"version":"go1.28.10","stable":true},{"version":"go1.27.20","stable":true}]`, 404, []string{"go1.29.0", "go1.28.10"}, false},
		{"api-error", `[{"version":"go1.29.0","stable":true}]`, 503, nil, true},
		{"empty-feed", `[]`, 404, nil, true},
		{"malformed-feed", `<html>unavailable</html>`, 404, nil, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/feed" {
					io.WriteString(w, tc.feed)
					return
				}
				if r.URL.Path == "/graphql" {
					io.WriteString(w, `{"data":{"repository":{"release":null}}}`)
					return
				}
				w.WriteHeader(tc.status)
			}))
			defer s.Close()
			b, err := cli(root, s.URL, "plan", "-feed", s.URL+"/feed")
			if (err != nil) != tc.fail {
				t.Fatalf("error=%v output=%s", err, b)
			}
			if tc.fail {
				return
			}
			var got struct {
				Versions []string
				Latest   string
			}
			if err := json.Unmarshal(b, &got); err != nil {
				t.Fatal(err)
			}
			if !reflect.DeepEqual(got.Versions, tc.want) {
				t.Fatalf("versions=%v, want %v", got.Versions, tc.want)
			}
			if got.Latest != tc.want[0] {
				t.Fatalf("latest=%s, want %s", got.Latest, tc.want[0])
			}
		})
	}
}

func TestDraftLookupFailures(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"graphql-http", http.StatusServiceUnavailable, "unavailable"},
		{"graphql-errors", http.StatusOK, `{"errors":[{"message":"lookup denied"}]}`},
		{"draft-details", http.StatusOK, `{"data":{"repository":{"release":{"databaseId":7}}}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := fixture(t)
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/feed":
					io.WriteString(w, `[{"version":"go1.29.0","stable":true}]`)
				case strings.Contains(r.URL.Path, "/releases/tags/"):
					w.WriteHeader(http.StatusNotFound)
				case r.URL.Path == "/graphql":
					w.WriteHeader(tc.status)
					io.WriteString(w, tc.body)
				case r.URL.Path == "/repos/test/go/releases/7":
					w.WriteHeader(http.StatusServiceUnavailable)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusInternalServerError)
				}
			}))
			defer s.Close()
			if output, err := cli(root, s.URL, "plan", "-feed", s.URL+"/feed"); err == nil {
				t.Fatalf("lookup failure treated as a missing release: %s", output)
			}
		})
	}
}

func TestPublicationLifecycle(t *testing.T) {
	for _, mode := range []string{"success", "latest", "resume-upload", "wrong-tree", "wrong-artifact"} {
		t.Run(mode, func(t *testing.T) {
			root := fixture(t)
			var mu sync.Mutex
			var draft map[string]any
			var assets []map[string]any
			var head string
			var mutations, publications, uploads int
			var creations, signedCommits int
			failedUpload := false
			var tag string
			var server *httptest.Server
			server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				mu.Lock()
				defer mu.Unlock()
				w.Header().Set("Content-Type", "application/json")
				respond := func(v any) { json.NewEncoder(w).Encode(v) }
				var body struct {
					Query     string
					Variables struct {
						Owner, Name, Tag string
						Input            struct {
							ExpectedHeadOID string
							FileChanges     struct{ Additions []any }
						}
					}
				}
				if r.URL.Path == "/graphql" {
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if strings.HasPrefix(body.Query, "query ReleaseByTag") {
						if body.Variables.Owner != "test" || body.Variables.Name != "go" || !strings.HasPrefix(body.Variables.Tag, "go1.29.0-atomix.") {
							t.Error("draft lookup must use the requested repository and pending tag")
						}
						var found any
						if draft != nil {
							found = map[string]any{"databaseId": 1}
						}
						respond(map[string]any{"data": map[string]any{"repository": map[string]any{"release": found}}})
						return
					}
				}
				if r.Method != "GET" {
					mutations++
				}
				switch {
				case r.URL.Path == "/feed":
					io.WriteString(w, `[{"version":"go1.29.0","stable":true}]`)
				case strings.Contains(r.URL.Path, "/releases/tags/"):
					if draft == nil || draft["draft"] == true {
						w.WriteHeader(404)
						return
					}
					draft["assets"] = assets
					respond(draft)
				case r.URL.Path == "/repos/test/go/releases/1" && r.Method == "GET":
					draft["assets"] = assets
					respond(draft)
				case strings.Contains(r.URL.Path, "/git/ref/tags/"):
					if draft == nil || draft["draft"] == true {
						w.WriteHeader(404)
						return
					}
					respond(map[string]any{"object": map[string]string{"sha": "signed", "type": "commit"}})
				case strings.Contains(r.URL.Path, "/git/ref/heads/"):
					if head == "" {
						w.WriteHeader(404)
						return
					}
					respond(map[string]any{"object": map[string]string{"sha": head, "type": "commit"}})
				case r.URL.Path == "/repos/test/go/git/refs":
					head = "upstream"
					w.WriteHeader(201)
				case r.URL.Path == "/graphql":
					signedCommits++
					if body.Variables.Input.ExpectedHeadOID != "upstream" {
						t.Error("candidate does not use pinned upstream parent")
					}
					head = "signed"
					tree := "tested-tree"
					if mode == "wrong-tree" {
						tree = "other-tree"
					}
					respond(map[string]any{"data": map[string]any{"createCommitOnBranch": map[string]any{"commit": map[string]any{"oid": "signed", "tree": map[string]string{"oid": tree}, "signature": map[string]bool{"isValid": true}}}}})
				case r.URL.Path == "/repos/test/go/git/commits/signed":
					io.WriteString(w, `{"tree":{"sha":"tested-tree"},"verification":{"verified":true}}`)
				case r.URL.Path == "/repos/test/go/releases" && r.Method == "POST":
					creations++
					if draft != nil {
						w.WriteHeader(http.StatusUnprocessableEntity)
						io.WriteString(w, `{"message":"release already exists"}`)
						return
					}
					if err := json.NewDecoder(r.Body).Decode(&draft); err != nil {
						t.Error(err)
					}
					if draft["target_commitish"] != "signed" || draft["draft"] != true {
						t.Error("release must start as a draft at the validated signed commit")
					}
					draft["id"] = 1
					draft["upload_url"] = server.URL + "/uploads{?name,label}"
					w.WriteHeader(201)
					respond(draft)
				case r.URL.Path == "/uploads":
					if mode == "resume-upload" && uploads == 2 && !failedUpload {
						failedUpload = true
						w.WriteHeader(503)
						return
					}
					b, _ := io.ReadAll(r.Body)
					a := map[string]any{"id": uploads + 1, "name": r.URL.Query().Get("name"), "size": len(b), "digest": fmt.Sprintf("sha256:%x", sha256.Sum256(b))}
					assets = append(assets, a)
					uploads++
					w.WriteHeader(201)
					respond(a)
				case r.URL.Path == "/repos/test/go/releases/1" && r.Method == "PATCH":
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					if body["make_latest"] != fmt.Sprint(mode == "latest") {
						t.Error("only the newest series may replace the latest release")
					}
					if uploads != 6 {
						t.Errorf("published with %d uploads", uploads)
					}
					draft["draft"] = false
					publications++
				case strings.Contains(r.URL.Path, "/git/refs/heads/") && r.Method == "DELETE":
					head = ""
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					w.WriteHeader(500)
				}
			}))
			defer server.Close()
			b, err := cli(root, server.URL, "plan", "-feed", server.URL+"/feed")
			if err != nil {
				t.Fatalf("plan: %v: %s", err, b)
			}
			var plan struct{ Recipe string }
			if err := json.Unmarshal(b, &plan); err != nil {
				t.Fatal(err)
			}
			tag = "go1.29.0-atomix." + plan.Recipe[:12]
			dir := t.TempDir()
			source := []byte("source archive fixture")
			write(t, filepath.Join(dir, tag+".src.tar.gz"), source)
			writeJSON(t, filepath.Join(dir, tag+".json"), map[string]string{"tree": "tested-tree"})
			writeJSON(t, filepath.Join(dir, "candidate.json"), map[string]any{
				"version": "go1.29.0", "tag": tag, "upstream": "upstream", "recipe": plan.Recipe, "bootstrap": "1.26.10", "tree": "tested-tree", "source_sha256": fmt.Sprintf("%x", sha256.Sum256(source)), "additions": []any{},
			})
			for _, p := range []string{"linux-amd64", "linux-arm64", "darwin-arm64"} {
				binary := []byte("binary fixture " + p)
				write(t, filepath.Join(dir, tag+"."+p+".tar.gz"), binary)
				writeJSON(t, filepath.Join(dir, p+".json"), map[string]string{"tag": tag, "tree": "tested-tree", "sha256": fmt.Sprintf("%x", sha256.Sum256(binary))})
			}
			if mode == "wrong-artifact" {
				write(t, filepath.Join(dir, tag+".linux-arm64.tar.gz"), []byte("different build"))
			}
			b, err = cli(root, server.URL, "publish", "-latest="+fmt.Sprint(mode == "latest"), "-out", dir)
			if mode == "wrong-tree" || mode == "wrong-artifact" {
				if err == nil {
					t.Fatalf("accepted %s", mode)
				}
				mu.Lock()
				defer mu.Unlock()
				if draft != nil || publications != 0 {
					t.Fatal("invalid candidate reached release creation")
				}
				if mode == "wrong-artifact" && mutations != 0 {
					t.Fatal("invalid artifact caused a remote mutation")
				}
				return
			}
			if mode == "resume-upload" {
				if err == nil {
					t.Fatal("upload error was swallowed")
				}
				mu.Lock()
				unpublished := draft["draft"] == true && publications == 0
				mu.Unlock()
				if !unpublished {
					t.Fatal("failed upload exposed a published release")
				}
				b, err = cli(root, server.URL, "publish", "-out", dir)
			}
			if err != nil {
				t.Fatalf("publish: %v: %s", err, b)
			}
			mu.Lock()
			before := mutations
			if creations != 1 || signedCommits != 1 {
				t.Errorf("publication created %d releases and %d signed commits; want one each", creations, signedCommits)
			}
			mu.Unlock()
			b, err = cli(root, server.URL, "publish", "-out", dir)
			if err != nil {
				t.Fatalf("repeat: %v: %s", err, b)
			}
			mu.Lock()
			after, count := mutations, publications
			mu.Unlock()
			if after != before || count != 1 {
				t.Fatalf("repeat mutated release: before=%d after=%d publications=%d", before, after, count)
			}
			b, err = cli(root, server.URL, "plan", "-feed", server.URL+"/feed")
			if err != nil {
				t.Fatalf("plan after publication: %v: %s", err, b)
			}
			var remaining struct{ Versions []string }
			if err := json.Unmarshal(b, &remaining); err != nil {
				t.Fatal(err)
			}
			if len(remaining.Versions) != 0 {
				t.Fatal("completed release was rediscovered")
			}
		})
	}
}
