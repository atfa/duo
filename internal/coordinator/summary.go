package coordinator

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/atfa/duo/internal/events"
	"github.com/atfa/duo/internal/project"
	"github.com/atfa/duo/internal/protocol"
)

// requestFinalSummary asks Austin to report the finished round to the human.
// It runs at DONE in both modes, which is the one point every completed session
// passes through: Fast after a passed verification and Goal after dual final
// approval, once the artifact is in the user's repository.
//
// Austin answers in its normal assistant message, and the timeline labels that
// `Austin → Human`, so the report reaches the human without the human having
// asked for it. Nothing here changes the phase machine or the delivered
// artifact: the session is already DONE and stays that way.
func (c *Coordinator) requestFinalSummary(ctx context.Context) {
	if !c.claimFinalSummary() {
		return
	}
	snap := c.project.Snapshot()
	if err := c.sendToAgent(ctx, protocol.Austin, protocol.Message{
		Version:   protocol.Version,
		Type:      protocol.MsgDuoNotice,
		From:      protocol.Duo,
		To:        protocol.Austin,
		Text:      c.finalSummaryPrompt(snap),
		Timestamp: time.Now().UnixMilli(),
	}); err != nil {
		// A disconnected Austin is recoverable, so the request is not consumed:
		// the next DONE transition for this round asks again.
		c.unclaimFinalSummary()
		c.logf("final summary request to Austin failed: %v", err)
		c.emit(events.KindError, protocol.Duo, protocol.Austin,
			"Duo could not ask Austin for the final summary: "+err.Error())
		return
	}
	// From here on the next Austin message is the report Duo promised the human,
	// so it is marked from this request instead of from its own first line.
	c.oweFinalSummary()
	c.recordEvent("summary_requested", map[string]any{
		"mode": snap.EffectiveMode().String(),
		"head": c.CurrentDelivery().FinalHead,
	})
	c.emit(events.KindSystem, protocol.Duo, protocol.Austin,
		"asking Austin to summarise this finished session for the human")
}

// claimFinalSummary reserves the one summary request a round may make. Delivery
// is retried by design — a duplicate final signature can re-enter the delivery
// transaction — and Austin must not be asked to report the same finished round
// twice.
func (c *Coordinator) claimFinalSummary() bool {
	c.summaryMu.Lock()
	defer c.summaryMu.Unlock()
	if c.summaryRequested {
		return false
	}
	c.summaryRequested = true
	return true
}

// unclaimFinalSummary gives the claim back. A request that could not be
// delivered stays owed, and the round the human opens after a finished one
// re-arms the request so it reports on its own delivery.
func (c *Coordinator) unclaimFinalSummary() {
	c.summaryMu.Lock()
	c.summaryRequested = false
	// The report a previous round owed belongs to that round's delivery: a new
	// round must not mark its first Austin message with it.
	c.summaryOwed = false
	c.summaryMu.Unlock()
}

// oweFinalSummary records that Duo has asked for the report and has not seen the
// answer yet. The next Austin message is the one Duo promised the human.
func (c *Coordinator) oweFinalSummary() {
	c.summaryMu.Lock()
	c.summaryOwed = true
	c.summaryMu.Unlock()
}

// takeSummaryOwed consumes the outstanding request. Only the first Austin
// message after the request is the report; anything after it is ordinary
// conversation.
func (c *Coordinator) takeSummaryOwed() bool {
	c.summaryMu.Lock()
	defer c.summaryMu.Unlock()
	owed := c.summaryOwed
	c.summaryOwed = false
	return owed
}

// finalSummaryPrompt asks Austin four questions about the finished round, and
// names the delivery facts that anchor the answers.
//
// The prompt asks for a report, not for a presentation of one: the interface
// marks that report for the human from its own state (see requestFinalSummary),
// so nothing about its first line is part of the contract. The prohibition on
// banners is there because a model left unprompted decorates a summary with
// rules and boxes, and a box drawn out of `=` characters is both ugly and — when
// a cost line comes first — unreadable to anything that tries to parse it.
//
// The questions are the contract, and they are deliberately not "describe the
// process and the results": a report written that way answers the prompt and
// still tells the human nothing they cannot read off the transcript. Asking for
// the result, then what the human does with it next, then the points of agreement
// and disagreement with Tony, then whether having two agents improved anything at
// all, forces the four answers the human actually wants and gives the last one a
// shape — yes or no, first — that cannot be satisfied by narrating the review.
//
// The handoff question is the one the human cannot reconstruct from the timeline:
// the timeline shows the work, not the state of the thing it produced. It is
// phrased as "what the human does with it" rather than "how to run it", because a
// task that delivers a document or a decision has no command to run, and a prompt
// that demands one gets invented steps instead.
// costLine renders the session's measured cost for the final report, in the shape
// the interface uses so the number in the report matches the number on screen. A
// session whose drivers measured nothing gets no line: the report then says nothing
// about cost rather than claiming it cost nothing.
func (c *Coordinator) costLine() string {
	ledger := c.UsageLedger()
	if ledger == nil {
		return ""
	}
	total := ledger.Total()
	return total.CostLine(c.language == "zh")
}

func (c *Coordinator) finalSummaryPrompt(snap project.Snapshot) string {
	d := c.CurrentDelivery()
	cost := c.costLine()
	set := c.workspace.Set()
	branch := strings.TrimSpace(d.TargetBranch)
	if branch == "" {
		branch = set.BaseBranch
	}
	applied := shortSHA(d.AppliedHead)
	if applied == "" {
		applied = shortSHA(d.FinalHead)
	}
	if c.language == "zh" {
		if cost != "" {
			cost = cost + "\n\n把这一行原样放在报告开头。"
		}
		return fmt.Sprintf("[Duo final summary request]\n本次会话已在 %s 模式下完成：最终产物已交付到 %s 的 %s 分支，HEAD 为 %s。\n\n%s\n\n首先直接回答用户原问题或说明其任务结果；问题要求操作步骤时给出具体步骤，说明未确认的地方。先给用户关心的内容，再报告必要的 Git 和协作事项。然后按顺序回答以下四个问题；不适用的部分简短说明，不要让模板取代用户的答案。每个问题先给结论，再给依据。\n\n1. 结果。仓库现在有哪些是它原来没有的：哪些行为变了，由哪些文件承载。一段话加一个文件清单，别的不要写。\n\n2. 接手。用户拿到这个产物之后怎么用它：从哪里开始、需要先准备什么、哪些限制或还没验证过的地方是用户该知道的。只写这次任务真正适用的内容，不要写成教程；如果产物不是拿来运行的东西，就写用户最先该看哪里。确实没有需要准备或需要注意的地方时，用一句话说明，不要编造步骤。\n\n3. 你和 Tony 达成一致的地方，以及没有达成一致的地方。先列出双方共同的结论；再逐条列出分歧：每条都要写明是谁持什么立场、什么证据判定了胜负、证据支持哪一方。如果你驳回了 Tony，直接写明并给出理由；如果你采纳了 Tony 的纠正，指出是哪一条。\n\n4. 两个 agent 有没有真的提高工作质量。第一句必须是「是」或「不是」，然后说明因为有两个 agent 而具体变好了什么：本来会漏发的发现、事后被证明量错的测量、被评审改掉的决定。如果这次协作没有产生价值，就直接说没有，不要硬凑。\n\n第 1、2、4 问各自控制在几句话内。第 3 问可以长一些，但只能长到这条分歧真正需要的程度。\n\n开头直接给结论——第一句话就是标题。不要画横幅、分隔线或标题框，也不要用 `=`、`-`、`|` 这类字符拼装饰；界面会自己把这份汇报标出来。\n\n不要复述会话过程——human 已经在时间线里读过了。\n\n在正常 assistant 回复中回答；Duo 会显示为 `Austin → Human`。不要使用 duo_send：该消息会发给 Tony，而不是用户。\n\n产物已经交付：不要编辑文件、提交或开始新工作。用户可随时提交后续任务，届时会以新一轮重新打开会话。", snap.EffectiveMode().Display(), set.Repository, branch, applied, cost)
	}
	if cost != "" {
		cost = cost + "\n\nReproduce this line at the top of your report."
	}
	return fmt.Sprintf(
		"[Duo final summary request]\nThis session is DONE in %s mode: the final artifact was delivered to %s on %s as %s.\n\n%s\n\nFirst directly answer the user’s original question or report the task result; provide concrete steps when requested and state what is unverified. Put what the user cares about before necessary Git and collaboration details. Then answer the following four questions in order, briefly noting any that do not apply; do not let the template replace the answer to the user. Lead each answer with its conclusion, not its evidence.\n\n1. Result. What the repository now has that it did not have before: the behaviour that changed, and the files that carry it. One paragraph and a file list, nothing else.\n\n2. Handoff. What the human does with what was delivered: where to start, what has to be ready first, and which limits or unverified parts they should know about. Write only what genuinely applies to this task, not a tutorial; if the deliverable is not something that runs, say what the human should look at first. When there is truly nothing to prepare or watch out for, say so in one sentence rather than inventing steps.\n\n3. Where you and Tony agreed, and where you did not. Name the conclusions you both reached. Then list every point of disagreement: for each one say which of you held which position, what evidence settled it, and which side that evidence supports. If you overruled Tony, say so plainly and give the reason. If you accepted a correction from Tony, name which one.\n\n4. Whether having two agents improved the work. Answer yes or no in the first sentence, then name what specifically got better because there were two: findings that would otherwise have shipped, measurements that turned out to be wrong, decisions that changed on review. If the collaboration produced nothing, say that rather than inventing value.\n\nKeep answers 1, 2 and 4 to a few sentences each. Answer 3 may run longer, but only to the length a disagreement actually needs.\n\nOpen with the conclusion: the first sentence is the title. Do not draw a banner, rule or title box, and do not decorate with runs of `=`, `-` or `|`; the interface marks this report for the human on its own.\n\nDo not narrate the session — the human has already read the timeline.\n\nReport in your normal assistant reply; Duo shows it as `Austin → Human`. Do not use duo_send: that message reaches Tony, not the human.\n\nThe artifact is already delivered: do not edit files, do not commit, and do not start new work. The human can submit a follow-up task at any time, which reopens the session as a fresh round.",
		snap.EffectiveMode().Display(), set.Repository, branch, applied, cost,
	)
}
