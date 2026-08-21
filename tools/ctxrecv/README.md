# ctxrecv

A `go/analysis` checker for the deadlock class fixed by
[#1677](https://github.com/github/gh-ost/pull/1677) (`rowCopyComplete`) and
[#1758](https://github.com/github/gh-ost/pull/1758) (`ghostTableMigrated`).

## The invariant

A channel whose every non-test send goes through `base.SendWithContext` has a
sender that *abandons the send* once the migration context is cancelled. A
blocking receive on such a channel, not inside a `select` offering an escape,
can therefore park forever: the only party that could wake it has given up.

Receives are reported unless the enclosing `select` has one of:

  - a `ctx.Done()` arm,
  - a `default:` arm (the receive is non-blocking), or
  - a timer arm (`time.After`, `ticker.C`) that bounds the wait.

Channels with any bare send, or that are closed, are exempt: those senders do
not abandon on cancellation, so the receive has an independent wakeup. Sends in
`_test.go` files are ignored when classifying, so a bare send in a test cannot
exempt a production channel.

## Known scope limits

Deliberately narrow, to stay at zero false positives:

  - Only *receives* are checked. A send with no receiver on some code path (the
    shape of [#1736](https://github.com/github/gh-ost/pull/1736)) is the absence
    of code and cannot be detected syntactically.
  - Only struct-field channels. Function-local channels are typically buffered
    and paired within one function.
  - Sends are classified per-package, so a send from another package is invisible.

## Why this is a separate module

gh-ost vendors its dependencies. Keeping this in its own module means
`golang.org/x/tools` never enters the parent `go.mod` or `vendor/` for a binary
the shipped `gh-ost` never uses. CI builds it on demand -- the same
install-at-CI-time pattern already used for golangci-lint. Do not merge this
into the parent module.

## Usage

    go -C tools/ctxrecv build -o /tmp/ctxrecv .
    /tmp/ctxrecv ./go/...
