package materials

import "fmt"

// RefinePrompt asks the LLM to extract phrase blocks from learner source text.
func RefinePrompt(kind, content string) string {
	return fmt.Sprintf(`You extract reusable workplace English phrase blocks from learner source text.

Kind: %s
Source:
%s

Reply with JSON only:
{"blocks":[{"intent_zh":"...","expression_en":"...","scene_tag":"standup|review|1on1|interview|casual","function_tag":"object|clarify|report|propose|agree|disagree|ask|summarize|defer|commit"}]}
Return at most 8 blocks. If nothing usable, return {"blocks":[]}.`, kind, content)
}
