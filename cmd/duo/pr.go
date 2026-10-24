package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"

	"github.com/atfa/duo/internal/delivery"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
)

// errGHMissing reports that gh is not installed, which is a documented
// degradation rather than a failure: Duo prints the same pull request instead.
var errGHMissing = errors.New("gh is not installed")

// prTitle is the pull request title: the plan's first meaningful line, so a
// reviewer reads the task rather than a session id.
func prTitle(snap sessionstore.Snapshot) string {
	for _, line := range strings.Split(snap.Plan, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "#>*- "))
		if line == "" {
			continue
		}
		if len(line) > 72 {
			line = strings.TrimSpace(line[:72]) + "…"
		}
		return line
	}
	return "Duo delivery " + snap.SessionID
}

// prBody renders the review chain Duo already holds: the plan version, the step
// queue, every signature with its evidence SHA, the verdict that released the
// artifact, and the diffstat. It is the same text whether gh opens the pull
// request or the human does.
func prBody(snap sessionstore.Snapshot, changes []delivery.Change) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Delivered by Duo from session `%s` (%s mode).\n\n", snap.SessionID, snap.EffectiveMode())

	b.WriteString("## Plan\n\n")
	fmt.Fprintf(&b, "Plan version %d.", snap.PlanVersion)
	if plan := strings.TrimSpace(snap.Plan); plan != "" {
		b.WriteString("\n\n")
		b.WriteString(plan)
	}
	b.WriteString("\n\n")

	if len(snap.Steps) > 0 {
		b.WriteString("## Steps\n\n")
		b.WriteString(snap.Steps.Render())
		b.WriteString("\n\n")
	}

	b.WriteString("## Sign-off\n\n")
	for _, agent := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		state := "not signed"
		if snap.Ready[agent] {
			state = "signed"
		}
		fmt.Fprintf(&b, "- %s: %s", agent, state)
		if evidence := strings.TrimSpace(snap.Evidence[agent]); evidence != "" {
			fmt.Fprintf(&b, " (evidence `%s`)", shortHash(evidence))
		}
		if note := strings.TrimSpace(snap.Notes[agent]); note != "" {
			fmt.Fprintf(&b, " — %s", note)
		}
		b.WriteString("\n")
	}
	if v := snap.Verification; v != nil && strings.TrimSpace(v.Status) != "" {
		fmt.Fprintf(&b, "- verification: %s for `%s`", v.Status, shortHash(v.Head))
		if note := strings.TrimSpace(v.Note); note != "" {
			fmt.Fprintf(&b, " — %s", note)
		}
		b.WriteString("\n")
	}
	b.WriteString("\n")

	b.WriteString("## Changes\n\n")
	b.WriteString("```\n")
	b.WriteString(delivery.Summary(changes))
	b.WriteString("\n```\n")
	return b.String()
}

// prHead is the branch a pull request must come from: the branch the artifact
// actually landed on. A retry can carry a freshly computed default, and naming a
// branch nothing was pushed to would open the request against the wrong head or
// fail after the push already happened.
func prHead(snap sessionstore.Snapshot, fallback string) string {
	if head := strings.TrimSpace(snap.Delivery.TargetBranch); head != "" {
		return head
	}
	return fallback
}

// openPullRequest asks gh to open the pull request, or prints the same body with
// what to do by hand when gh is not installed. Duo never talks to a hosting API
// itself: gh is the only hosting dependency, and only when it is present.
func openPullRequest(ctx context.Context, repo, base, head, title, body string, out io.Writer) error {
	gh, err := exec.LookPath("gh")
	if err != nil {
		printPRFallback(out, base, head, body)
		return nil
	}

	cmd := exec.CommandContext(ctx, gh, "pr", "create",
		"--base", base, "--head", head, "--title", title, "--body-file", "-")
	cmd.Dir = repo
	cmd.Stdin = strings.NewReader(body)
	var stdout, stderr bytes.Buffer
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		message := strings.TrimSpace(stderr.String())
		if message == "" {
			message = err.Error()
		}
		return fmt.Errorf("gh pr create: %s", message)
	}
	if url := strings.TrimSpace(stdout.String()); url != "" {
		fmt.Fprintln(out, url)
	}
	return nil
}

// printPRFallback is the degradation path: the branch is already pushed, so the
// human only has to open the pull request, and Duo hands over the body it would
// have submitted.
func printPRFallback(out io.Writer, base, head, body string) {
	fmt.Fprintln(out, "gh is not installed, so Duo did not open the pull request itself.")
	fmt.Fprintln(out)
	fmt.Fprintf(out, "The branch %s is pushed. Open a pull request from %s into %s.\n", head, head, base)
	fmt.Fprintln(out)
	fmt.Fprintln(out, "Pull request body:")
	fmt.Fprintln(out)
	fmt.Fprintln(out, body)
}

// shortHash trims a commit id the way the rest of the interface does.
func shortHash(value string) string {
	value = strings.TrimSpace(value)
	if len(value) > 10 {
		return value[:10]
	}
	return value
}
