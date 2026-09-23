## What changed


## Why it belongs in Duo


## Verification

- [ ] `go test ./...`
- [ ] `go vet ./...`
- [ ] `go build ./cmd/duo`
- [ ] Real two-agent test performed when Pi bridge/coordinator behavior changed

## Design check

- [ ] Duo Core remains the single source of shared project truth
- [ ] No automatic merge into the human branch
- [ ] New complexity is justified by an observed collaboration problem
