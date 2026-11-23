# Atomix Go releases

Daily CI selects the latest stable patch of the two newest Go release lines and of Go 1.27. When Go 1.29 is stable, for example, the targets are 1.29.x, 1.28.x and 1.27.x. Each version appears once in the release plan.

`recipe.json` pins the patch base and selects the latest stock Go 1.27.x as the direct bootstrap. The planner checks the bootstrap version against the Go archive and passes it to every job. Revisit the bootstrap when an upstream target requires a newer compiler. [Go bootstrap requirements](https://go.dev/doc/install/source#go14)

CI applies `atomix.patch` to upstream tags and regenerates SSA. Tags and toolchain versions use `goX.Y.Z-atomix.<recipe digest>` to distinguish build caches. The digest covers the resolved bootstrap, build inputs and automation, excluding docs and tests.

Linux amd64, Linux arm64, and macOS arm64 build from one source archive, run compiler tests, and verify native intrinsics and identity constant folding without fallback calls.

Releases target a GitHub-signed commit matching the tested tree. Drafts resume on retry and publish after every asset verifies. Published releases are immutable; only the newest series is marked latest. Pull requests never publish.

From the repository root, use the latest stock Go 1.27.x and a discovered upstream tag in `GO_VERSION`.

```sh
export GOWORK=off GOTOOLCHAIN=local
go -C .github/release test -race -timeout 120s ./...
go -C .github/release vet ./...
go -C .github/release run . prepare -version "$GO_VERSION" -out /tmp/atomix-candidate
```

Use a fresh candidate directory. `-offline` uses fetched tags. After patch or bootstrap changes, prepare each selected version and run compiler and intrinsic checks.
