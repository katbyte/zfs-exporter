# Contributing

Issues and pull requests are welcome.

- `make check-all` runs everything CI runs. See [development](docs/development.md).
- A reading that looks wrong is easiest to fix with a capture of the machine. See [when a reading looks wrong](docs/development.md#when-a-reading-looks-wrong).
- Anonymize a capture before attaching it.

## Releases

- Changes collect under "Unreleased" in the [changelog](CHANGELOG.md).
- A version is tagged when a batch is worth upgrading for.
- Breaking changes are grouped into one release, not shipped one at a time.
- Pushing a `v*` tag publishes the Linux programs, the Docker image and the Homebrew formula.
