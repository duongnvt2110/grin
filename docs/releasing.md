# Releasing Grin

This repository publishes macOS and Linux binaries through GitHub Actions and
GoReleaser OSS. A release is created when a tag matching `v*` is pushed.

## Before the first release

Confirm that:

- the repository is available at `duongnvt2110/grin`;
- the default branch is `main`;
- GitHub Actions has **Read and write permissions** for repository contents;
- `.github/workflows/ci.yml` and `.github/workflows/release.yml` are present;
- `CHANGELOG.md` contains the intended release history;
- the working tree contains the intended source and documentation.

The release workflow uses GitHub's automatic `GITHUB_TOKEN`. No personal token
is required for the workflow.

## Validate locally

Run the same checks used by CI before tagging:

```zsh
sh -n scripts/install.sh
go test ./...
go test -race ./...
go vet ./...
go mod verify
go build -buildvcs=false ./cmd/grin
```

Remove any temporary root binary after the build if it is not intended for the
repository:

```zsh
rm -f ./grin
```

## Publish a release

Push the tested main branch first:

```zsh
git switch main
git status
git push -u origin main
```

Create a new semantic version tag. Do not reuse a tag for different contents:

```zsh
VERSION=v0.1.0
git tag "$VERSION"
git push origin "$VERSION"
```

Run `Grin CI` on the target commit before creating the release tag. The tag
starts the separate `Grin Release` workflow, which uses GoReleaser to publish
release notes, archives, and the checksum file.

GoReleaser keeps the GitHub-native changelog and adds a short link to
`CHANGELOG.md` in the release notes. Update the `Unreleased` section before
the next version, then move its entries under the new version heading after
the release is published.

## Verify the GitHub Release

The release must contain:

```text
grin_Darwin_arm64.tar.gz
grin_Darwin_amd64.tar.gz
grin_Linux_arm64.tar.gz
grin_Linux_amd64.tar.gz
checksums.txt
```

Verify the published version and installer on a supported machine:

```zsh
VERSION=v0.1.0
INSTALL_DIR=$(mktemp -d)/bin
curl -fsSL https://raw.githubusercontent.com/duongnvt2110/grin/main/scripts/install.sh \
  | sh -s -- --version "$VERSION" --dir "$INSTALL_DIR"
"$INSTALL_DIR/grin" --version
```

The output should report the tagged version. Also check the local health
endpoint after starting Grin:

```zsh
grin --workspace /tmp/grin-demo
curl -fsS http://127.0.0.1:8765/healthz
```

## Release failures

If the workflow fails, fix the source and publish the next patch version, for
example `v0.1.1`. Do not move an existing tag to different source contents.
