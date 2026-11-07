// Copyright 2026 Hayabusa Cloud Co., Ltd.
// Use of this source code is governed by a BSD-style license that can be found in the LICENSE file.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type buildResult struct {
	Tag    string `json:"tag"`
	Tree   string `json:"tree"`
	SHA256 string `json:"sha256"`
}

func publish(a *api, dir, digest string, latest bool) error {
	var c candidate
	if err := readJSON(filepath.Join(dir, "candidate.json"), &c); err != nil {
		return err
	}
	if c.Recipe != digest {
		return errors.New("candidate was prepared with a different release recipe")
	}
	sourceHash, err := fileDigest(filepath.Join(dir, c.Tag+".src.tar.gz"))
	if err != nil {
		return err
	}
	if sourceHash != c.SourceSHA256 {
		return errors.New("candidate source archive checksum mismatch")
	}
	for _, platform := range platforms {
		var result buildResult
		if err := readJSON(filepath.Join(dir, platform+".json"), &result); err != nil {
			return err
		}
		h, err := fileDigest(filepath.Join(dir, c.Tag+"."+platform+".tar.gz"))
		if err != nil {
			return err
		}
		if result.Tag != c.Tag || result.Tree != c.Tree || result.SHA256 != h {
			return fmt.Errorf("%s artifact does not match the validated source tree", platform)
		}
	}
	var sums strings.Builder
	for _, name := range requiredAssets(c.Tag) {
		if name == "SHA256SUMS" {
			continue
		}
		h, err := fileDigest(filepath.Join(dir, name))
		if err != nil {
			return err
		}
		fmt.Fprintf(&sums, "%s  %s\n", h, name)
	}
	if err := os.WriteFile(filepath.Join(dir, "SHA256SUMS"), []byte(sums.String()), 0644); err != nil {
		return err
	}
	r, err := a.getRelease(c.Tag)
	if err != nil {
		return err
	}
	branch := "atomix-candidate/" + c.Tag
	if r == nil {
		sha, err := a.signedCandidate(c, branch)
		if err != nil {
			return err
		}
		r = new(release)
		body := fmt.Sprintf("Atomix compiler based on %s.\n\nSource tree: `%s`\nRecipe: `%s`\nBootstrap: Go %s\n\nPlatforms: linux/amd64, linux/arm64, darwin/arm64.\n", c.Version, c.Tree, c.Recipe, c.Bootstrap)
		if err := a.request("POST", "/repos/"+a.repo+"/releases", map[string]any{
			"tag_name": c.Tag, "target_commitish": sha, "name": c.Tag, "body": body, "draft": true, "make_latest": "false",
		}, r); err != nil {
			return err
		}
	}
	// A draft may not have a tag yet; once a tag exists, its tree is authoritative.
	sha, err := a.ref("tags/" + c.Tag)
	if err != nil {
		return err
	}
	if sha == "" {
		sha = r.Target
	}
	if err := a.verifyCommit(sha, c.Tree); err != nil {
		return err
	}
	for _, name := range requiredAssets(c.Tag) {
		path := filepath.Join(dir, name)
		h, err := fileDigest(path)
		if err != nil {
			return err
		}
		var existing *asset
		for i := range r.Assets {
			if r.Assets[i].Name == name {
				existing = &r.Assets[i]
				break
			}
		}
		if existing != nil {
			if existing.Digest == "sha256:"+h {
				continue
			}
			if !r.Draft {
				return fmt.Errorf("published asset %s differs; refusing to replace it", name)
			}
			if err := a.request("DELETE", fmt.Sprintf("/repos/%s/releases/assets/%d", a.repo, existing.ID), nil, nil); err != nil {
				return err
			}
		}
		if !r.Draft {
			return fmt.Errorf("published release is incomplete: missing %s", name)
		}
		if err := a.upload(r.UploadURL, path, h); err != nil {
			return err
		}
	}
	if r.Draft {
		if err := a.request("PATCH", fmt.Sprintf("/repos/%s/releases/%d", a.repo, r.ID), map[string]any{"draft": false, "make_latest": strconv.FormatBool(latest)}, nil); err != nil {
			return err
		}
	}
	// Delete only this recipe's temporary ref, and only if it still names our commit.
	head, err := a.ref("heads/" + branch)
	if err != nil {
		return err
	}
	if head == sha {
		return a.request("DELETE", "/repos/"+a.repo+"/git/refs/heads/"+branch, nil, nil)
	}
	return nil
}

func (a *api) ref(name string) (string, error) {
	var r struct {
		Object struct {
			SHA  string `json:"sha"`
			Type string `json:"type"`
		}
	}
	err := a.request("GET", "/repos/"+a.repo+"/git/ref/"+name, nil, &r)
	var e *apiError
	if errors.As(err, &e) && e.status == http.StatusNotFound {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	if r.Object.Type != "commit" {
		return "", fmt.Errorf("%s must refer directly to a commit", name)
	}
	return r.Object.SHA, nil
}

func (a *api) verifyCommit(sha, tree string) error {
	var c struct {
		Tree struct {
			SHA string `json:"sha"`
		}
		Verification struct{ Verified bool }
	}
	if err := a.request("GET", "/repos/"+a.repo+"/git/commits/"+sha, nil, &c); err != nil {
		return err
	}
	if c.Tree.SHA != tree {
		return errors.New("release commit does not contain the validated source tree")
	}
	if !c.Verification.Verified {
		return errors.New("release source commit is not verified")
	}
	return nil
}

func (a *api) signedCandidate(c candidate, branch string) (string, error) {
	// Recover a tag created by an earlier interrupted publication.
	sha, err := a.ref("tags/" + c.Tag)
	if err != nil {
		return "", err
	}
	if sha != "" {
		return sha, a.verifyCommit(sha, c.Tree)
	}
	head, err := a.ref("heads/" + branch)
	if err != nil {
		return "", err
	}
	if head == "" {
		if err := a.request("POST", "/repos/"+a.repo+"/git/refs", map[string]string{"ref": "refs/heads/" + branch, "sha": c.Upstream}, nil); err != nil {
			return "", err
		}
		head = c.Upstream
	}
	if head != c.Upstream {
		return head, a.verifyCommit(head, c.Tree)
	}
	const query = `mutation($input: CreateCommitOnBranchInput!) {
  createCommitOnBranch(input: $input) { commit { oid tree { oid } signature { isValid } } }
}`
	var result struct {
		CreateCommitOnBranch struct {
			Commit struct {
				OID       string
				Tree      struct{ OID string }
				Signature *struct{ IsValid bool }
			}
		}
	}
	input := map[string]any{
		"branch":          map[string]string{"repositoryNameWithOwner": a.repo, "refName": branch},
		"expectedHeadOid": c.Upstream,
		"message":         map[string]string{"headline": "cmd/compile: apply Atomix recipe to " + c.Version},
		"fileChanges":     map[string]any{"additions": c.Additions},
	}
	if err := a.query(query, map[string]any{"input": input}, &result); err != nil {
		return "", err
	}
	commit := result.CreateCommitOnBranch.Commit
	if commit.OID == "" || commit.Tree.OID != c.Tree || commit.Signature == nil || !commit.Signature.IsValid {
		return "", errors.New("signed candidate does not match the validated source tree")
	}
	return commit.OID, nil
}

func (a *api) upload(template, path, digest string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	u, _, _ := strings.Cut(template, "{")
	u += "?name=" + url.QueryEscape(filepath.Base(path))
	req, err := http.NewRequest("POST", u, f)
	if err != nil {
		return err
	}
	req.ContentLength = info.Size()
	req.Header.Set("Authorization", "Bearer "+a.token)
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := a.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("upload %s: HTTP %d: %s", filepath.Base(path), resp.StatusCode, b)
	}
	var got asset
	if err := json.NewDecoder(resp.Body).Decode(&got); err != nil {
		return err
	}
	if got.Size != info.Size() || got.Digest != "sha256:"+digest {
		return fmt.Errorf("uploaded asset checksum mismatch: %s", got.Name)
	}
	return nil
}
