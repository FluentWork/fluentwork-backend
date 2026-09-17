package corpus

import "context"

// StarterBlocks is the development starter corpus.
//
// The phrases are hand-picked for one property: a developer can say them into a
// microphone and the ASR transcript will match, so a B7 badge fires on demand.
// They are not production data and must never be seeded outside a local
// environment.
//
// One list, two callers: cmd/corpus-seed (seeding a known device explicitly) and
// StarterProvisioner (a guest getting a starter corpus on their first session).
func StarterBlocks() []BatchAcceptBlock {
	return []BatchAcceptBlock{
		{IntentZH: "说明阻塞点", ExpressionEN: "I'm blocked on the API review.", AnchorUserSaid: "I am blocked on the API review.", SceneTag: "standup", FunctionTag: "report"},
		{IntentZH: "把会议收尾", ExpressionEN: "Let's wrap up.", AnchorUserSaid: "let's wrap up the meeting", SceneTag: "review", FunctionTag: "summarize"},
		{IntentZH: "推动上线", ExpressionEN: "Let's ship it.", AnchorUserSaid: "let's ship it", SceneTag: "review", FunctionTag: "commit"},
		{IntentZH: "请求澄清", ExpressionEN: "Could you clarify what you mean by that?", AnchorUserSaid: "could you clarify", SceneTag: "1on1", FunctionTag: "clarify"},
		{IntentZH: "委婉拒绝延期", ExpressionEN: "I'd rather not push the deadline.", AnchorUserSaid: "I don't want to push the deadline", SceneTag: "1on1", FunctionTag: "disagree"},
		{IntentZH: "主动提议", ExpressionEN: "How about we pair on this tomorrow?", AnchorUserSaid: "how about we pair on this tomorrow", SceneTag: "casual", FunctionTag: "propose"},
		{IntentZH: "承认不确定", ExpressionEN: "I'm not 100% sure yet, but I'll confirm by EOD.", AnchorUserSaid: "I'm not sure yet", SceneTag: "standup", FunctionTag: "defer"},
		{IntentZH: "总结结论", ExpressionEN: "Bottom line: we'll ship next Tuesday.", AnchorUserSaid: "bottom line", SceneTag: "review", FunctionTag: "summarize"},
		{IntentZH: "请求反馈", ExpressionEN: "Does that work for you?", AnchorUserSaid: "does that work for you", SceneTag: "1on1", FunctionTag: "ask"},
		{IntentZH: "礼貌结束", ExpressionEN: "Thanks for your time today.", AnchorUserSaid: "thanks for your time", SceneTag: "casual", FunctionTag: "agree"},
	}
}

// starterSourceSession names the synthetic session starter blocks belong to.
const starterSourceSession = "dev-starter-corpus"

// StarterProvisioner gives a brand-new learner a starter corpus, so a physical
// device can fire badges without anyone hunting for its guest id.
//
// The friction it removes is a silent one: a corpus is scoped per user, so
// seeding a fixed device id helps only that device — a phone authenticates as
// its own guest, badges then simply never fire, and nothing anywhere says why
// (dev-up.sh used to carry a warning about exactly this).
//
// Two guardrails keep it honest: it is wired only in development (cmd/app-server
// checks APP_ENV before attaching it), and it does nothing once the learner owns
// a single block — a starter corpus is a starting point, not a topping-up.
type StarterProvisioner struct {
	Service *Service
}

// ProvisionStarterCorpus seeds the starter list when the learner has no blocks,
// returning how many were added. That is zero for every learner past their first
// session, which is also what makes it safe to call on every session create.
func (p StarterProvisioner) ProvisionStarterCorpus(ctx context.Context, userID string) (int, error) {
	if p.Service == nil {
		return 0, nil
	}
	existing, err := p.Service.ListBlocks(ctx, ListBlocksRequest{UserID: userID, Limit: 1})
	if err != nil {
		return 0, err
	}
	if len(existing.Items) > 0 {
		return 0, nil
	}
	resp, err := p.Service.BatchAccept(ctx, userID, BatchAcceptRequest{
		SourceSessionID: starterSourceSession,
		Blocks:          StarterBlocks(),
	})
	if err != nil {
		return 0, err
	}
	return resp.AcceptedCount, nil
}
