# Security policy

## Supported versions

Security fixes go into the latest minor release. Upgrade to it before you report a problem.

## Reporting a vulnerability

Don't open a public issue for a security problem. Report it privately through GitHub's
[private vulnerability reporting](https://github.com/hmdsefi/faultline/security/advisories/new),
or by email to hdyousefi@gmail.com.

faultline runs inside your tests, so most security problems come from input it reads: an artifact
folder, a `schedule.json` file or a seed list that makes faultline, `faultline render` or the
timeline page run code, write outside the artifact folder, or hang.

Include:

- the faultline version and the Go version
- what goes wrong and what an attacker gains
- the smallest input that reproduces it

You'll get a reply within 7 days. Once the problem is confirmed, the fix ships in a release, and
the release notes credit you unless you'd rather not be named.
