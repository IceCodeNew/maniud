# any-llm-go repin checklist

Maniud imports `github.com/mozilla-ai/any-llm-go` and replaces it with the reviewed `github.com/IceCodeNew/any-llm-go` aggregate. Dependency automation must not change this replace. A reviewed repin pull request must complete every check below.

## Current pin

- Ref: `refs/heads/IceCodeNew/stack`
- Commit: `1c5364355b59e4054d5a0cfe16f141043229795d`
- Pseudo-version: `v0.0.0-20260830222028-1c5364355b59`
- Module sum: `h1:Ntpv9B8Eo7hdpmMaZbC34HpDjDiYdRcIyCUzalO26wI=`
- `go.mod` sum: `h1:lu+b9+Rwiypo4C2VOFBS2HbtOay2j4tbpqyDsIRvJjA=`

Do not use or create `refs/heads/stack`.

## Repin procedure

1. Record the proposed full fork commit, commit time, pseudo-version, and target upstream ancestor. Verify the exact remote `refs/heads/IceCodeNew/stack` value through a freshly fetched Git ref.
2. Inspect the commit signatures and upstream ancestry. Record any aggregate commit that does not descend from the intended upstream commit.
3. In a temporary module outside the Maniud repository, add the same module-path require and fork replace proposed for `go.mod`. Use a fresh module cache and run `go mod download -json`, `go mod verify`, and `go list -m -json all`.
4. Confirm that `Origin.Ref` is `refs/heads/IceCodeNew/stack`, `Origin.Hash` is the proposed full commit, and the downloaded module and `go.mod` sums match the review record.
5. Recheck the OpenAI, OpenAI-compatible, and dedicated DeepSeek constructors. Confirm endpoint defaults, caller-supplied HTTP client use, context cancellation, timeout behavior, retry count, cross-origin redirect policy, and DeepSeek JSON-schema preprocessing.
6. Run the real-adapter protocol fixtures. They must cover non-streaming completion, `reasoning_effort=none`, omitted absent assistant `tool_calls`, explicit empty `logit_bias`, Responses `instructions`, Azure normalized Responses parity, structured output, duplicate or invalid choices, empty content, content filtering, truncation, and reported-model mismatch.
7. Run `go mod verify`, repository lint, affected tests, race tests, and the full branch gate. Attach exact commands and results to the pull request.
8. Keep Renovate disabled for both the upstream module path and fork target. Merge only through the normal review process.

## Removing the replace

Remove the fork replace only after an upstream formal release contains every fork behavior used by Maniud. Run the same clean-module resolution, provider fixtures, affected tests, race tests, and full branch gate against that release. The migration pull request must identify the upstream commits or release notes for each required behavior and must not combine the dependency change with unrelated product work.
