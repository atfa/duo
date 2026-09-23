# GitHub publishing notes

Suggested repository:

```text
atfa/duo
```

Suggested description:

> Two peer coding agents for Pi: shared plans, live collaboration, isolated Git worktrees, cross-review and joint sign-off.

Suggested topics:

```text
ai-agent
coding-agent
multi-agent
pi
agent-orchestration
peer-to-peer
golang
git-worktree
llm
local-first
```

Current release tag:

```text
v0.3.1
```

Release title:

```text
Duo v0.3.1 — runtime hardening and binary installers
```

Pushing a `v*` tag builds macOS and Linux archives for amd64 and arm64 and publishes them through GitHub Actions. Users can install the latest release with:

```bash
curl -fsSL https://raw.githubusercontent.com/atfa/duo/main/scripts/install-release.sh | bash
```

Recommended repository settings before announcement:

- enable Issues;
- enable Discussions if you want design feedback;
- enable GitHub private vulnerability reporting if available;
- keep branch protection lightweight while the project is experimental;
- add the topics above for discoverability.

The product name **Duo** is intentionally short, but it is a generic name used by unrelated projects. Keep the repository subtitle/tagline visible in the README and GitHub description so search results immediately distinguish this project.
