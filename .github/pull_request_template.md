## What this changes

<!-- What it does and why. If it fixes an issue, "Fixes #123". -->

## What you ran

<!--
Green CI is not evidence about Windows: the Go tests run on ubuntu, and that
gap has already hidden a real bug (Winsock error numbers vs POSIX errno in
probe.classify). Please say what you actually ran, and where.
-->

- [ ] `go test ./...`
- [ ] `cd ui && npm test`
- [ ] Ran on **Windows** — required if this touches sockets, credentials, the
      filesystem or the service wrapper
- [ ] Ran the relevant sections of [installer/ACCEPTANCE.md](../installer/ACCEPTANCE.md)
      — required if this touches the installer or the service wrapper.
      Which sections: <!-- e.g. §1–§3, §7 -->

## Notes for the reviewer

<!-- Anything surprising, any trade-off you made, anything you are unsure about. -->

---

By opening this PR you agree your contribution is licensed under the
[Apache License 2.0](../LICENSE), per section 5. There is no CLA.
