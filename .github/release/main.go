// Copyright 2026 Hayabusa Cloud Co., Ltd.
// Use of this source code is governed by a BSD-style license that can be found in the LICENSE file.

// Command release derives and publishes Atomix toolchains from upstream Go tags.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	goversion "go/version"
	"io"
	"io/fs"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"time"
)

var stableVersion = regexp.MustCompile(`^go[0-9]+\.[0-9]+\.[0-9]+$`)
var platforms = []string{"linux-amd64", "linux-arm64", "darwin-arm64"}

type recipe struct {
	Base      string `json:"base"`
	Bootstrap string `json:"bootstrap"`
}

type manifest struct {
	Version   string `json:"version"`
	Tag       string `json:"tag"`
	Upstream  string `json:"upstream"`
	Recipe    string `json:"recipe"`
	Bootstrap string `json:"bootstrap"`
}

type addition struct {
	Path     string `json:"path"`
	Contents string `json:"contents"`
}

type candidate struct {
	manifest
	Tree         string     `json:"tree"`
	SourceSHA256 string     `json:"source_sha256"`
	Additions    []addition `json:"additions"`
}

type release struct {
	ID        int64   `json:"id"`
	Draft     bool    `json:"draft"`
	Target    string  `json:"target_commitish"`
	UploadURL string  `json:"upload_url"`
	Assets    []asset `json:"assets"`
}

type asset struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Size   int64  `json:"size"`
	Digest string `json:"digest"`
}

type api struct {
	base, graph, repo, token string
	client                   *http.Client
}

type apiError struct {
	status  int
	message string
}

func (e *apiError) Error() string { return fmt.Sprintf("GitHub HTTP %d: %s", e.status, e.message) }

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) < 2 {
		return errors.New("usage: release plan|prepare|record|publish [flags]")
	}
	f := flag.NewFlagSet(os.Args[1], flag.ContinueOnError)
	root := f.String("root", "../..", "compiler repository")
	repo := f.String("repo", os.Getenv("GITHUB_REPOSITORY"), "GitHub owner/repository")
	feed := f.String("feed", "https://go.dev/dl/?mode=json", "upstream release feed")
	base := f.String("api", "https://api.github.com", "GitHub REST endpoint")
	graph := f.String("graphql", "https://api.github.com/graphql", "GitHub GraphQL endpoint")
	version := f.String("version", "", "upstream version, such as go1.27.3")
	out := f.String("out", "", "candidate or publication directory")
	platform := f.String("platform", "", "validated target platform")
	latest := f.Bool("latest", false, "mark the newest discovered version as the latest release")
	offline := f.Bool("offline", false, "prepare using already fetched upstream tags")
	if err := f.Parse(os.Args[2:]); err != nil {
		return err
	}
	abs, err := filepath.Abs(*root)
	if err != nil {
		return err
	}
	var cfg recipe
	if err := readJSON(filepath.Join(abs, ".github/release/recipe.json"), &cfg); err != nil {
		return err
	}
	digest, err := recipeDigest(abs)
	if err != nil {
		return err
	}
	a := &api{
		base:   *base,
		graph:  *graph,
		repo:   *repo,
		token:  os.Getenv("GH_TOKEN"),
		client: &http.Client{Timeout: 10 * time.Minute},
	}
	switch os.Args[1] {
	case "plan":
		return plan(a, *feed, cfg, digest)
	case "prepare":
		if !stableVersion.MatchString(*version) || *out == "" {
			return errors.New("prepare requires -version goX.Y.Z and -out")
		}
		return prepare(abs, *out, *version, cfg, digest, *offline)
	case "publish":
		if *out == "" {
			return errors.New("publish requires -out with the candidate and build artifacts")
		}
		return publish(a, *out, digest, *latest)
	case "record":
		if !slices.Contains(platforms, *platform) {
			return errors.New("record requires a supported -platform")
		}
		var c candidate
		if err := readJSON(filepath.Join(*out, "candidate.json"), &c); err != nil {
			return err
		}
		h, err := fileDigest(filepath.Join(*out, c.Tag+"."+*platform+".tar.gz"))
		if err != nil {
			return err
		}
		return writeJSON(filepath.Join(*out, *platform+".json"), buildResult{c.Tag, c.Tree, h})
	default:
		return fmt.Errorf("unknown command %q", os.Args[1])
	}
}

// The identity covers the patch, recipe, driver, smoke checks, and workflow.
// Tests and prose do not change the toolchain recipe.
func recipeDigest(root string) (string, error) {
	h := sha256.New()
	dir := filepath.Join(root, ".github/release")
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		if strings.HasSuffix(path, "_test.go") || strings.HasSuffix(path, ".md") {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.ToSlash(rel), len(data))
		h.Write(data)
		return nil
	})
	if err != nil {
		return "", err
	}
	workflows, err := filepath.Glob(filepath.Join(root, ".github/workflows/release*.yml"))
	if err != nil {
		return "", err
	}
	for _, path := range workflows {
		data, err := os.ReadFile(path)
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00%d\x00", filepath.Base(path), len(data))
		h.Write(data)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

func plan(a *api, feed string, cfg recipe, digest string) error {
	resp, err := a.client.Get(feed)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Go release feed: HTTP %d", resp.StatusCode)
	}
	var releases []struct {
		Version string
		Stable  bool
	}
	if err := json.NewDecoder(resp.Body).Decode(&releases); err != nil {
		return err
	}
	var versions []string
	for _, r := range releases {
		if r.Stable && stableVersion.MatchString(r.Version) {
			versions = append(versions, r.Version)
		}
	}
	slices.SortFunc(versions, func(a, b string) int {
		return goversion.Compare(b, a)
	})
	seen := map[string]bool{}
	pending := []string{}
	for _, v := range versions {
		series := v[:strings.LastIndexByte(v, '.')]
		if seen[series] {
			continue
		}
		if len(seen) == 2 {
			break
		}
		seen[series] = true
		tag := v + "-atomix." + digest[:12]
		r, err := a.getRelease(tag)
		if err != nil {
			return err
		}
		if r == nil || !complete(r, tag) {
			pending = append(pending, v)
		}
	}
	if len(seen) == 0 {
		return errors.New("Go feed contains no stable versions")
	}
	return json.NewEncoder(os.Stdout).Encode(struct {
		Versions  []string `json:"versions"`
		Bootstrap string   `json:"bootstrap"`
		Recipe    string   `json:"recipe"`
		Latest    string   `json:"latest"`
	}{pending, cfg.Bootstrap, digest, versions[0]})
}

func requiredAssets(tag string) []string {
	names := []string{tag + ".src.tar.gz", tag + ".json", "SHA256SUMS"}
	for _, p := range platforms {
		names = append(names, tag+"."+p+".tar.gz")
	}
	return names
}

func complete(r *release, tag string) bool {
	if r.Draft {
		return false
	}
	for _, name := range requiredAssets(tag) {
		if !slices.ContainsFunc(r.Assets, func(a asset) bool { return a.Name == name && a.Size > 0 }) {
			return false
		}
	}
	return true
}

func prepare(root, output, version string, cfg recipe, digest string, offline bool) error {
	out, err := filepath.Abs(output)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(out, 0755); err != nil {
		return err
	}
	upstream := version
	if !offline {
		if _, err := command(root, "git", "fetch", "--no-tags", "https://github.com/golang/go.git", "refs/tags/"+version); err != nil {
			return err
		}
		upstream = "FETCH_HEAD"
	}
	sha, err := command(root, "git", "rev-parse", upstream+"^{commit}")
	if err != nil {
		return err
	}
	if _, err := command(root, "git", "cat-file", "-e", cfg.Base+"^{commit}"); err != nil {
		return fmt.Errorf("missing patch base %s: %w", cfg.Base, err)
	}
	source := filepath.Join(out, "worktree")
	if _, err := command(root, "git", "worktree", "add", "--detach", source, sha); err != nil {
		return err
	}
	defer func() { _, _ = command(root, "git", "worktree", "remove", "--force", source) }()
	if _, err := command(source, "git", "apply", "--3way", "--index", filepath.Join(root, ".github/release/atomix.patch")); err != nil {
		return err
	}
	if _, err := command(filepath.Join(source, "src/cmd/compile/internal/ssa/_gen"), "go", "run", "."); err != nil {
		return err
	}
	m := manifest{
		Version:   version,
		Tag:       version + "-atomix." + digest[:12],
		Upstream:  sha,
		Recipe:    digest,
		Bootstrap: cfg.Bootstrap,
	}
	// Go release tool IDs use VERSION, so each recipe needs its own cache identity.
	versionFile := filepath.Join(source, "VERSION")
	versionData, err := os.ReadFile(versionFile)
	if err != nil {
		return err
	}
	_, metadata, _ := bytes.Cut(versionData, []byte("\n"))
	if err := os.WriteFile(versionFile, append([]byte(m.Tag+"\n"), metadata...), 0644); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(source, "ATOMIX.json"), m); err != nil {
		return err
	}
	if _, err := command(source, "git", "add", "src/cmd/compile", "ATOMIX.json", "VERSION"); err != nil {
		return err
	}
	tree, err := command(source, "git", "write-tree")
	if err != nil {
		return err
	}
	c := candidate{manifest: m, Tree: tree}
	paths, err := command(source, "git", "diff", "--cached", "--name-only", "--diff-filter=AM", "-z")
	if err != nil {
		return err
	}
	for _, path := range strings.Split(strings.TrimRight(paths, "\x00"), "\x00") {
		data, err := os.ReadFile(filepath.Join(source, path))
		if err != nil {
			return err
		}
		c.Additions = append(c.Additions, addition{path, base64.StdEncoding.EncodeToString(data)})
	}
	archive := filepath.Join(out, m.Tag+".src.tar.gz")
	timestamp, err := command(source, "git", "show", "-s", "--format=%ct", sha)
	if err != nil {
		return err
	}
	if _, err := command(source, "git", "archive", "--format=tar.gz", "--prefix=go/", "--mtime=@"+timestamp, "-o", archive, tree); err != nil {
		return err
	}
	c.SourceSHA256, err = fileDigest(archive)
	if err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(out, "candidate.json"), c); err != nil {
		return err
	}
	return writeJSON(filepath.Join(out, m.Tag+".json"), struct {
		manifest
		Tree string `json:"tree"`
	}{m, tree})
}

func (a *api) getRelease(tag string) (*release, error) {
	var r release
	err := a.request("GET", "/repos/"+a.repo+"/releases/tags/"+url.PathEscape(tag), nil, &r)
	if err == nil {
		return &r, nil
	}
	var e *apiError
	if !errors.As(err, &e) || e.status != http.StatusNotFound {
		return nil, err
	}
	// REST tag lookup only resolves published releases. A pending draft tag
	// must be resolved through GraphQL before fetching its assets by REST ID.
	owner, name, ok := strings.Cut(a.repo, "/")
	if !ok {
		return nil, errors.New("release lookup requires an owner/repository")
	}
	const query = `query ReleaseByTag($owner: String!, $name: String!, $tag: String!) {
  repository(owner: $owner, name: $name) { release(tagName: $tag) { databaseId } }
}`
	var found struct {
		Repository *struct {
			Release *struct{ DatabaseID int64 }
		}
	}
	if err := a.query(query, map[string]string{"owner": owner, "name": name, "tag": tag}, &found); err != nil {
		return nil, err
	}
	if found.Repository == nil {
		return nil, errors.New("release lookup did not return the repository")
	}
	if found.Repository.Release == nil {
		return nil, nil
	}
	err = a.request("GET", fmt.Sprintf("/repos/%s/releases/%d", a.repo, found.Repository.Release.DatabaseID), nil, &r)
	return &r, err
}

func (a *api) query(query string, variables, result any) error {
	var response struct {
		Data   json.RawMessage
		Errors []struct{ Message string }
	}
	if err := a.request("POST", a.graph, map[string]any{"query": query, "variables": variables}, &response); err != nil {
		return err
	}
	if len(response.Errors) != 0 {
		return fmt.Errorf("GitHub GraphQL: %s", response.Errors[0].Message)
	}
	return json.Unmarshal(response.Data, result)
}

func (a *api) request(method, path string, input, output any) error {
	var body io.Reader
	if input != nil {
		data, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(data)
	}
	u := path
	if !strings.HasPrefix(u, "http") {
		u = a.base + path
	}
	req, err := http.NewRequest(method, u, body)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("Content-Type", "application/json")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return &apiError{resp.StatusCode, string(data)}
	}
	if output != nil {
		return json.NewDecoder(resp.Body).Decode(output)
	}
	return nil
}

func command(dir, name string, args ...string) (string, error) {
	c := exec.Command(name, args...)
	c.Dir = dir
	c.Stderr = os.Stderr
	b, err := c.Output()
	if err != nil {
		return "", fmt.Errorf("%s %v: %w", name, args, err)
	}
	return strings.TrimSpace(string(b)), nil
}

func readJSON(path string, v any) error {
	b, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(b, v)
}

func writeJSON(path string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0644)
}

func fileDigest(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
