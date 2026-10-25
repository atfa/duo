package session

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"

	"github.com/atfa/duo/internal/harness"
	"github.com/atfa/duo/internal/observer"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
	"github.com/atfa/duo/internal/sessionstore"
	"github.com/atfa/duo/internal/workspace"
)

// observerReportsKept is how many reports the panel can look back on. The
// observer runs only when a human asks for one, so a short history is enough and
// a growing log is exactly what the old timer-driven observer got wrong.
const observerReportsKept = 3

// observerGitTimeout bounds the git work one report samples. A status report must
// never hang behind a busy worktree: whatever git cannot answer in time is simply
// missing from the live block.
const observerGitTimeout = 2 * time.Second

// maxLiveBranchFiles bounds how many changed files one branch contributes to the
// live block. The overlap is computed from the full sets, so the display cut can
// never hide a collision.
const maxLiveBranchFiles = 12

// maxLiveNoteRunes bounds a sign-off note or verification note in the live block.
const maxLiveNoteRunes = 200

// observerRequest is one queued request: a status report when question is empty,
// or the human's question for the observer to answer.
type observerRequest struct {
	question string
}

// maxPlanRunes bounds the plan text the live block carries. Goal-mode plans are
// the task's decomposition, so the bound is generous and the cut falls back to
// the last newline (see clipPlan).
const maxPlanRunes = 2400

// RequestObserverReport asks the observer for a fresh status report.
//
// It returns immediately. A model call may take its whole timeout, and no
// frontend may be blocked behind one, so the request is handed to a worker. A
// request that arrives while another is waiting replaces it: the panel keeps one
// in-flight call and one waiting slot, so pressing keys cannot fan out requests.
func (s *Service) RequestObserverReport() {
	s.requestObserver(observerRequest{})
}

// RequestObserverAnswer asks the observer one question the human typed.
//
// A blank question is not a request, so nothing is sent and no notice appears. A
// question that arrives while another request is waiting replaces it rather than
// being dropped: the human typed it last and expects it answered.
func (s *Service) RequestObserverAnswer(question string) {
	question = observer.NormalizeQuestion(question)
	if question == "" {
		return
	}
	s.requestObserver(observerRequest{question: question})
}

// requestObserver queues one request and wakes the worker.
func (s *Service) requestObserver(req observerRequest) {
	s.observerReportMu.Lock()
	trigger := s.observerTrigger
	if trigger != nil {
		s.observerPending = &req
	}
	s.observerReportMu.Unlock()
	if trigger == nil {
		return
	}
	select {
	case trigger <- struct{}{}:
	default:
	}
}

// startObserverWorker runs the request queue. Its context is derived from the
// session's own, so closing the session cancels an in-flight model call.
func (s *Service) startObserverWorker() {
	if s.observerCancel != nil {
		return
	}
	ctx, cancel := context.WithCancel(s.ctx)
	trigger := make(chan struct{}, 1)
	s.observerReportMu.Lock()
	s.observerTrigger = trigger
	s.observerReportMu.Unlock()
	s.observerCancel = cancel
	go s.runObserverReports(ctx, trigger)
}

func (s *Service) runObserverReports(ctx context.Context, trigger <-chan struct{}) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-trigger:
			req, ok := s.takeObserverRequest()
			if !ok {
				continue
			}
			s.analyzeObserverReport(ctx, req)
		}
	}
}

// takeObserverRequest claims the waiting request and marks it in flight.
//
// Marking it here, not when the request was queued, is what keeps the panel
// honest: the elapsed time and the question it reports belong to the call that is
// actually running.
func (s *Service) takeObserverRequest() (observerRequest, bool) {
	s.observerReportMu.Lock()
	defer s.observerReportMu.Unlock()
	if s.observerPending == nil {
		return observerRequest{}, false
	}
	req := *s.observerPending
	s.observerPending = nil
	s.observerAnalyzing = true
	s.observerRequestedAt = time.Now()
	s.observerAnswering = req.question
	return req, true
}

// analyzeObserverReport performs one request and files the result.
//
// The live block is rendered here, at request time, so a queued re-run reports
// what is true when it runs rather than what was true when the key was pressed.
func (s *Service) analyzeObserverReport(ctx context.Context, req observerRequest) {
	defer func() {
		s.observerReportMu.Lock()
		s.observerAnalyzing = false
		s.observerAnswering = ""
		s.observerReportMu.Unlock()
	}()

	s.mu.RLock()
	obs := s.observer
	s.mu.RUnlock()
	if obs == nil {
		return
	}

	var (
		text string
		err  error
	)
	if req.question != "" {
		text, err = obs.Answer(ctx, req.question)
	} else {
		text, err = obs.Report(ctx)
	}
	if strings.TrimSpace(text) != "" {
		s.setObserverReport(text, req.question)
		// A successful report clears a stale notice; a truncation notice is set
		// again immediately below.
		s.clearObserverNotice()
	}
	if err != nil {
		s.observerLogf()("observer: %v", err)
	}
}

// setObserverReport files one report, newest first, keeping a short history.
func (s *Service) setObserverReport(text, question string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	s.observerReportMu.Lock()
	defer s.observerReportMu.Unlock()
	report := ObserverReport{At: time.Now(), Question: question, Text: text}
	s.observerReports = append([]ObserverReport{report}, s.observerReports...)
	if len(s.observerReports) > observerReportsKept {
		s.observerReports = s.observerReports[:observerReportsKept]
	}
}

// observerReportSnapshot projects the report panel state.
func (s *Service) observerReportSnapshot() ([]ObserverReport, bool, time.Time, string) {
	s.observerReportMu.RLock()
	defer s.observerReportMu.RUnlock()
	return append([]ObserverReport(nil), s.observerReports...),
		s.observerAnalyzing, s.observerRequestedAt, s.observerAnswering
}

// observerLogf is the one place an observer failure becomes a log line and a
// panel notice. Missing-key state has a dedicated, actionable rendering in
// ObserverView, so that template is logged without becoming a notice.
func (s *Service) observerLogf() func(string, ...any) {
	return func(format string, args ...any) {
		message := fmt.Sprintf(format, args...)
		if !strings.Contains(message, "no API key in DUO_OBSERVER_API_KEY") {
			s.setObserverNotice(message)
		}
		s.logf()("%s", message)
	}
}

// observerLiveContext renders the runtime state a report's "Doing now" section
// is written from: mode, phase, the current step, and per agent what it is doing
// at this instant. It is the only input that can answer "what are they doing
// right now", because a running tool never becomes a message.
func (s *Service) observerLiveContext() string {
	// Branch facts come from git, so they are sampled before the session lock is
	// taken: a report that waited for a worktree would hold the session.
	austinBranch, tonyBranch := s.observerBranches(context.Background())

	s.mu.RLock()
	defer s.mu.RUnlock()
	v := s.view()
	now := time.Now()

	var b strings.Builder
	snap := v.Project
	fmt.Fprintf(&b, "mode=%s phase=%s", snap.EffectiveMode().Display(), snap.Phase)
	if snap.PlanVersion > 0 {
		fmt.Fprintf(&b, " planVersion=%d", snap.PlanVersion)
	}
	if snap.EffectiveMode() == project.ModeStep && len(snap.Steps) > 0 {
		if open := project.FirstOpenStep(snap.Steps); open <= len(snap.Steps) {
			fmt.Fprintf(&b, " step=%d/%d", open, len(snap.Steps))
		} else {
			b.WriteString(" step=all landed")
		}
	}
	b.WriteString("\n")
	// Who is blocking, and what each branch actually contains: the observer sees
	// promises and evidence side by side, so these facts are what let a report say
	// why nothing is moving instead of guessing from the log. Everything here is
	// omitted when the session has nothing to say (Fast mode has no sign-offs, and
	// unknown git facts are silence, never "empty").
	if snap.EffectiveMode() != project.ModeFast {
		fmt.Fprintf(&b, "Sign-offs: Austin ready=%t%s%s, Tony ready=%t%s%s\n",
			snap.Ready[protocol.Austin], signoffNote(snap.Notes[protocol.Austin]), signoffEvidence(snap.Evidence[protocol.Austin]),
			snap.Ready[protocol.Tony], signoffNote(snap.Notes[protocol.Tony]), signoffEvidence(snap.Evidence[protocol.Tony]))
	}
	if line := verificationLine(snap.Verification); line != "" {
		b.WriteString(line)
		b.WriteString("\n")
	}
	if line := branchLine(austinBranch, tonyBranch); line != "" {
		b.WriteString(line)
		b.WriteString("\n")
	}
	if line := changeLine(austinBranch, tonyBranch); line != "" {
		b.WriteString(line)
		b.WriteString("\n")
	}
	if line := usageLine(v.Usage); line != "" {
		b.WriteString(line)
		b.WriteString("\n")
	}
	if command := strings.TrimSpace(v.TestCommand); command != "" {
		fmt.Fprintf(&b, "Test command: %s\n", clipRunes(command, maxLiveNoteRunes))
	}
	// The shared plan text and the step queue are the only source for how the
	// task was decomposed and how far it has come, so they are rendered here
	// (bounded), not left for the report model to guess from the log.
	if plan := strings.TrimSpace(snap.Plan); plan != "" {
		fmt.Fprintf(&b, "\nShared plan (version %d):\n%s\n", snap.PlanVersion, clipPlan(plan))
	}
	if len(snap.Steps) > 0 {
		open := project.FirstOpenStep(snap.Steps)
		fmt.Fprintf(&b, "\nStep queue (%d step(s), %d landed", len(snap.Steps), landedSteps(snap.Steps))
		// FirstOpenStep returns len+1 once every step has landed, so a naive
		// "current step N of M" runs past the end exactly when the last step is
		// sitting in review or integration — the moment a progress report is most
		// likely to be opened. The header uses the same source, so the block never
		// carries two readings of one number.
		if open <= len(snap.Steps) {
			fmt.Fprintf(&b, ", current step %d of %d):\n", open, len(snap.Steps))
		} else {
			b.WriteString(", all steps landed):\n")
		}
		b.WriteString(snap.Steps.Render())
	}
	for _, id := range []protocol.AgentID{protocol.Austin, protocol.Tony} {
		fmt.Fprintf(&b, "%s: %s\n", id, agentActivity(v.Agents[id], now))
	}
	return strings.TrimRight(b.String(), "\n")
}

// clipPlan shortens a plan to maxPlanRunes. The cut falls back to the last
// newline when there is one, so a cut never leaves half a numbered item: in Goal
// mode the plan's items are the decomposition, and a truncated item undercounts
// the steps a report can cite. A plan without newlines is cut at the bound, and
// every cut keeps the truncation marker so the report cannot read a partial plan
// as the whole decomposition.
func clipPlan(plan string) string {
	clipped, cut := clipRunesMarked(plan, maxPlanRunes)
	if !cut {
		return clipped
	}
	// Drop the marker before the newline search: the cut line is partial and
	// must be dropped whole, not kept up to its last interior newline.
	body := strings.TrimSuffix(clipped, "…")
	if i := strings.LastIndex(body, "\n"); i >= 0 {
		return body[:i+1] + "…"
	}
	return clipped
}

// landedSteps counts the queue's done steps for the live block's one-line tally.
func landedSteps(steps project.Steps) int {
	n := 0
	for _, st := range steps {
		if st.Done {
			n++
		}
	}
	return n
}

// agentActivity describes one agent from its live runtime state.
//
// The distinction that matters is running versus finished: the tracker keeps the
// last tool after it ends, so "tool_started set and depth above zero" is what
// makes a tool current.
func agentActivity(av AgentView, now time.Time) string {
	rt := av.Runtime
	var parts []string
	running := rt.Tool != "" && rt.ToolDepth > 0
	switch {
	case running:
		parts = append(parts, "busy running tool "+rt.Tool+toolDetail(rt.ToolDetail)+
			" for "+shortDuration(now.Sub(rt.ToolStarted)))
	case rt.Busy && rt.ProviderActive:
		parts = append(parts, "busy waiting on the model")
	case rt.Busy:
		parts = append(parts, "busy")
	default:
		parts = append(parts, "idle for "+shortDuration(now.Sub(rt.LastActivity)))
	}
	if rt.Busy && !rt.TurnStarted.IsZero() {
		parts = append(parts, fmt.Sprintf("turn started %s ago with %d tool call(s)", shortDuration(now.Sub(rt.TurnStarted)), rt.Tools))
	}
	if tail := strings.TrimSpace(rt.StreamTail); tail != "" {
		parts = append(parts, "currently writing: "+clipRunes(tail, 240))
	}
	if len(rt.Recent) > 0 {
		parts = append(parts, "last tools: "+recentTools(rt.Recent))
	}
	if rt.ContextWindow > 0 {
		parts = append(parts, fmt.Sprintf("context %dk/%dk", rt.ContextTokens/1000, rt.ContextWindow/1000))
	}
	if err := strings.TrimSpace(rt.LastError); err != "" {
		parts = append(parts, "last error: "+clipRunes(err, 200))
	}
	return strings.Join(parts, "; ")
}

// toolDetail renders a tool's arguments, clipped so one long command cannot
// dominate the block.
func toolDetail(detail string) string {
	detail = strings.Join(strings.Fields(strings.TrimSpace(detail)), " ")
	if detail == "" {
		return ""
	}
	return "(" + clipRunes(detail, 200) + ")"
}

// recentTools renders the finished tool trail of the current turn.
func recentTools(notes []harness.ToolNote) string {
	out := make([]string, 0, len(notes))
	for _, note := range notes {
		mark := "ok"
		if !note.OK {
			mark = "failed"
		}
		out = append(out, fmt.Sprintf("%s %s %s", note.Name, mark, shortDuration(note.Duration)))
	}
	return strings.Join(out, ", ")
}

// shortDuration renders a duration the way a status line reads.
func shortDuration(d time.Duration) string {
	switch {
	case d < 0:
		return "0s"
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm%02ds", int(d.Minutes()), int(d.Seconds())%60)
	default:
		return fmt.Sprintf("%dh%02dm", int(d.Hours()), int(d.Minutes())%60)
	}
}

// observerBranch is what one agent's branch actually contains: its head, how far
// it is ahead of the session's base commit, whether it has uncommitted work, and
// the files it changed. ok is false when git could not answer in time, and the
// live block then says nothing about that branch rather than calling it empty.
type observerBranch struct {
	head  string
	ahead int
	dirty bool
	files []string
	ok    bool
}

// observerBranches samples both worktrees for one report. It is best-effort by
// design: the observer is advisory, so a git failure costs one line of context
// instead of producing a notice the human has to dismiss.
func (s *Service) observerBranches(ctx context.Context) (observerBranch, observerBranch) {
	s.mu.RLock()
	ws := s.ws
	s.mu.RUnlock()
	if ws == nil {
		return observerBranch{}, observerBranch{}
	}
	ctx, cancel := context.WithTimeout(ctx, observerGitTimeout)
	defer cancel()
	return observerBranchOf(ctx, ws, protocol.Austin), observerBranchOf(ctx, ws, protocol.Tony)
}

func observerBranchOf(ctx context.Context, ws *workspace.GitManager, id protocol.AgentID) observerBranch {
	status, err := ws.Status(ctx, id)
	if err != nil {
		return observerBranch{}
	}
	branch := observerBranch{head: status.Head, ahead: status.Ahead, dirty: status.Dirty, ok: true}
	if status.Ahead > 0 {
		branch.files = gitChangedFiles(ctx, status.Path, ws.Set().BaseCommit)
	}
	return branch
}

// gitChangedFiles lists the files a branch committed relative to base.
//
// Uncommitted work is reported by the worktree's dirty flag instead, because what
// the two branches will bring to integration is their commits.
func gitChangedFiles(ctx context.Context, dir, base string) []string {
	if dir == "" || strings.TrimSpace(base) == "" {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, observerGitTimeout)
	defer cancel()
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "diff", "--name-only", base+"..HEAD").Output()
	if err != nil {
		return nil
	}
	var files []string
	for _, line := range strings.Split(string(out), "\n") {
		if name := strings.TrimSpace(line); name != "" {
			files = append(files, name)
		}
	}
	return files
}

// branchLine names what each branch holds, so a report can tell a claim from a
// commit. A branch git could not describe is left out entirely.
func branchLine(austin, tony observerBranch) string {
	var parts []string
	for _, entry := range []struct {
		name   string
		branch observerBranch
	}{{"Austin", austin}, {"Tony", tony}} {
		if !entry.branch.ok {
			continue
		}
		parts = append(parts, fmt.Sprintf("%s head=%s ahead=%d dirty=%t",
			entry.name, shortLiveCommit(entry.branch.head), entry.branch.ahead, entry.branch.dirty))
	}
	if len(parts) == 0 {
		return ""
	}
	return "Worktrees: " + strings.Join(parts, "; ")
}

// changeLine names the files each branch changed and, explicitly, the ones both
// changed: that overlap is the one integration risk Duo can see before the merge.
// A branch git could not describe is left out, never rendered as empty, and the
// overlap is stated only when both branches are actually known.
func changeLine(austin, tony observerBranch) string {
	if (!austin.ok || len(austin.files) == 0) && (!tony.ok || len(tony.files) == 0) {
		return ""
	}
	var parts []string
	if austin.ok {
		parts = append(parts, "Austin "+fileSetText(austin.files))
	}
	if tony.ok {
		parts = append(parts, "Tony "+fileSetText(tony.files))
	}
	line := "Changed files vs base: " + strings.Join(parts, "; ")
	if austin.ok && tony.ok {
		line += "; overlap " + fileSetText(overlappingFiles(austin.files, tony.files))
	}
	return line
}

// fileSetText renders one file set compactly, bounding the display only. The
// count is always the real one, so a cut list cannot read as the whole set.
func fileSetText(files []string) string {
	if len(files) == 0 {
		return "none"
	}
	shown := files
	if len(shown) > maxLiveBranchFiles {
		shown = shown[:maxLiveBranchFiles]
	}
	text := strings.Join(shown, ", ")
	if extra := len(files) - len(shown); extra > 0 {
		text += fmt.Sprintf(", … +%d more", extra)
	}
	return fmt.Sprintf("[%s] (%d file(s))", text, len(files))
}

// overlappingFiles returns the files both branches changed, in the order they
// appear in austin. It compares the full sets, so a bounded display can never
// hide a collision.
func overlappingFiles(austin, tony []string) []string {
	if len(austin) == 0 || len(tony) == 0 {
		return nil
	}
	inTony := make(map[string]bool, len(tony))
	for _, name := range tony {
		inTony[name] = true
	}
	var shared []string
	for _, name := range austin {
		if inTony[name] {
			shared = append(shared, name)
		}
	}
	return shared
}

// verificationLine renders the Fast-mode verdict and the head it is bound to.
func verificationLine(v project.Verification) string {
	if v.Status == project.VerificationNone {
		return ""
	}
	line := "Verification: " + v.Label()
	if head := shortLiveCommit(v.Head); head != "" {
		line += " target=" + head
	}
	if note := liveNote(v.Note); note != "" {
		line += " (note: " + note + ")"
	}
	return line
}

// usageLine renders the session's measured cost, which is the one fact that says
// whether a slow round is expensive as well as slow. It stays silent until
// something has been measured, so a report never shows a cost of zero.
func usageLine(usage *sessionstore.UsageLedger) string {
	if usage == nil {
		return ""
	}
	total := usage.Total()
	if total.Turns == 0 && total.TotalTokens == 0 && total.CostUSD == 0 {
		return ""
	}
	parts := []string{fmt.Sprintf("%d turn(s)", total.Turns), compactCount(total.TotalTokens) + " token(s)"}
	if total.CostUSD > 0 {
		parts = append(parts, fmt.Sprintf("$%.2f", total.CostUSD))
	}
	return "Usage so far: " + strings.Join(parts, ", ")
}

func signoffNote(note string) string {
	if note = liveNote(note); note == "" {
		return ""
	}
	return " (note: " + note + ")"
}

func signoffEvidence(evidence string) string {
	if evidence = liveNote(evidence); evidence == "" {
		return ""
	}
	return " (evidence: " + evidence + ")"
}

// liveNote collapses a free-text note to one bounded line, so a multi-line
// sign-off cannot make the live block look like several facts.
func liveNote(note string) string {
	return clipRunes(strings.Join(strings.Fields(note), " "), maxLiveNoteRunes)
}

// shortLiveCommit shortens a commit hash the way the status line does.
func shortLiveCommit(head string) string {
	head = strings.TrimSpace(head)
	if len(head) > 7 {
		return head[:7]
	}
	return head
}

// compactCount renders a large count in the short form a status line uses.
func compactCount(n int) string {
	switch {
	case n >= 1_000_000:
		return fmt.Sprintf("%.1fM", float64(n)/1_000_000)
	case n >= 1_000:
		return fmt.Sprintf("%.1fk", float64(n)/1_000)
	default:
		return fmt.Sprintf("%d", n)
	}
}

// clipRunes shortens text to at most limit runes, marking the cut.
func clipRunes(text string, limit int) string {
	clipped, _ := clipRunesMarked(text, limit)
	return clipped
}

// clipRunesMarked is clipRunes that also reports whether it cut, so a caller can
// decide what the truncation marker means.
func clipRunesMarked(text string, limit int) (string, bool) {
	if limit <= 0 {
		return "", text != ""
	}
	runes := []rune(text)
	if len(runes) <= limit {
		return text, false
	}
	return string(runes[:limit]) + "…", true
}
