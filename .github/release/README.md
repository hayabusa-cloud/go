# Atomix Go releases

Daily CI checks the latest stable patch in the two newest Go minor series and builds pending releases. New series need no branch or workflow edits.

`recipe.json` pins the patch base and bootstrap compiler. CI applies `atomix.patch` to upstream tags and regenerates SSA. Tags and toolchain versions use `goX.Y.Z-atomix.<recipe digest>`, separating their build caches from upstream Go and other recipes. The digest covers build inputs and automation, excluding docs and tests.

Linux amd64, Linux arm64, and macOS arm64 build from one source archive, run compiler tests, and verify native intrinsics and identity constant folding without fallback calls.

Releases target a GitHub-signed commit matching the tested tree. Drafts resume on retry and publish after every asset verifies. Published releases are immutable; only the newest series is marked latest. Pull requests never publish.

From the repository root, using the pinned bootstrap compiler:

```sh
export GOWORK=off GOTOOLCHAIN=local
go -C .github/release test -race -timeout 120s ./...
go -C .github/release vet ./...
go -C .github/release run . prepare -version go1.27.3 -out /tmp/atomix-candidate
```

Use a fresh candidate directory; `-offline` uses fetched tags. After patch or bootstrap changes, prepare both supported versions and run compiler and intrinsic checks.
