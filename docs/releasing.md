# Releasing gomcdc

Merging a pull request into `master` updates the next release draft with Release
Drafter. The next version defaults to a patch; the `enhancement` label selects a
minor version. Until the first GitHub Release exists, the latest v2 tag supplies
the comparison baseline and the next patch version.

To release:

1. Review the draft's version and notes in GitHub Releases.
2. Open Actions → Release → Run workflow, selecting `master`.

The workflow tests that commit with the existing Go/Linux/macOS CI matrix and
self-MC/DC baselines, builds Linux and macOS archives for amd64 and arm64, and
checks the packaged version and SHA-256 checksums. It uploads the archives to the
draft, then publishes it and creates its tag at the tested commit. `gomcdc version`
reports the release tag in the packaged binaries.

If the draft targets an older commit, run Actions → Release Drafter on `master`
before running Release. Failed validation or packaging leaves the release as a
draft. A failed upload can be retried by rerunning the workflow.

Pull requests that change release configuration build the same archives without
publishing. GoReleaser can also build them locally:

```sh
GOMCDC_RELEASE_TAG=v2.0.0-dev goreleaser release --snapshot --clean
```
