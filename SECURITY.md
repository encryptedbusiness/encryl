# Security policy

## Supported versions

| Version | Status | Notes |
| ------- | ------ | ----- |
| 1.1.x | Supported | Current release. Bebop is the only client codec. |
| 1.0.x | Supported | Pre-Bebop release; upgrade to 1.1 for the client codec. |
| 0.x | Unsupported | Pre-release hardening line. Superseded by 1.0.0. |

Version 0 was the pre-release line. Version 1.1.0 is the current release;
version 1.0.0 remains supported. The module path did not change. If you are
running a version 0 build, see `docs/migration-v0-v1.md`.

## Reporting a vulnerability

Do not open a public issue. Report privately to the maintainers listed in
`CODEOWNERS`, or use GitHub's private vulnerability reporting on
`github.com/encryptedbusiness/encryl`.

Include: affected version or commit, reproduction steps, and the security
impact. We aim to acknowledge within 3 working days.

## Threat model and known constraints

`docs/threat-model.md` documents the trust boundaries. Two constraints deserve
repeating here:

1. **io_uring is the only backend and is for trusted processes only.** io_uring
   request fields cannot be inspected by seccomp, and the subsystem has a long
   history of privilege-escalation bugs. Google's kernel security team reported
   that 60% of the Linux kernel exploits it received in one year used io_uring,
   and has restricted it in Android, ChromeOS, and Google production. The
   server is Linux-only, drops privileges after ring setup where possible, and
   applies `IORING_REGISTER_RESTRICTIONS`. See
   `docs/adr/0012-io-uring-only.md`.
2. **TLS terminates upstream.** The library speaks plaintext `ws://` only and
   provides no confidentiality. Deploy it behind a TLS-terminating load
   balancer or reverse proxy and never expose the plaintext listener to an
   untrusted network. See `docs/adr/0013-tls-terminates-upstream.md`.
