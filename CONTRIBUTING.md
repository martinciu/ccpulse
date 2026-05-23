# Contributing to ccpulse

Thanks for your interest in ccpulse.

ccpulse is a small tool maintained by one person in spare time. It's
deliberately opinionated and tightly scoped — a single TUI view, no runtime
`git` dependency, a fixed set of commands — and that focus is what keeps it
coherent.

## Pull requests

**I'm not accepting external pull requests.** Please don't open one — I'd hate
for you to spend hours on a change I can't merge. With a single maintainer and
limited review bandwidth, taking on outside code would mean either rushing
reviews or letting PRs go stale, and the tight scope is something I want to
protect.

That's not a brush-off — there's a better way to help.

## Issues are welcome

Bug reports **and** feature requests are genuinely welcome, and they shape
where ccpulse goes next. Opening an issue is the real way to influence the
tool — far more effective than a pull request would be.

- **Found a bug?** Open an issue.
- **Want a feature, or a different default?** Open an issue and make the case.

### Filing a good issue

A little detail goes a long way:

- What you ran, and what you expected versus what actually happened.
- The output of `ccpulse version`.
- The output of `ccpulse doctor` (it summarises config, cache, credential,
  and hook state).
- Your OS and terminal.
- Minimal steps to reproduce, if you can.

## Want to change it yourself?

ccpulse is MIT-licensed — fork it freely and make it your own. The
[Build from source](README.md#build-from-source) section of the README has
everything you need to get a local build going.

## Security

Please don't file security reports publicly. Report vulnerabilities privately
through GitHub's advisory flow — see [`SECURITY.md`](SECURITY.md).

## Code of conduct

This project follows the [Contributor Covenant](CODE_OF_CONDUCT.md). By taking
part, you agree to uphold it.
