package conversation

import "strings"

// IncompleteDetector 检测用户话轮是否为未完成句
// 用于 B8 卡壳救援机制的触发条件之一
type IncompleteDetector struct{}

// NewIncompleteDetector 创建未完成句检测器
func NewIncompleteDetector() *IncompleteDetector {
	return &IncompleteDetector{}
}

// IsIncomplete 判定用户话轮是否为未完成句
// 判定规则：
// 1. 语义截断：以连接词结尾（but, and, because, so, if）
// 2. 缺谓语：少于 3 个单词且无明显动词
// 3. 以省略号或破折号结尾
func (d *IncompleteDetector) IsIncomplete(userText string) bool {
	text := strings.TrimSpace(strings.ToLower(userText))
	if text == "" {
		return false
	}

	// 规则 1：以连接词结尾（语义截断）
	conjunctions := []string{" but", " and", " because", " so", " if", " when", " although", " though"}
	for _, conj := range conjunctions {
		if strings.HasSuffix(text, conj) {
			return true
		}
	}

	// 规则 2：以省略号或破折号结尾
	if strings.HasSuffix(text, "...") || strings.HasSuffix(text, "--") {
		return true
	}

	// 规则 3：少于 3 词且无明显动词（缺谓语）
	// 单词不视为不完整（简短回应如 "okay", "yes", "sure" 是完整的）
	words := strings.Fields(text)
	if len(words) >= 2 && len(words) < 3 {
		hasVerb := d.containsVerb(text)
		if !hasVerb {
			return true
		}
	}

	return false
}

// containsVerb 检查文本中是否包含常见动词（简化版）
func (d *IncompleteDetector) containsVerb(text string) bool {
	// 常见 be 动词和助动词
	verbs := []string{
		" is ", " are ", " was ", " were ", " am ",
		" have ", " has ", " had ",
		" do ", " does ", " did ",
		" will ", " would ", " can ", " could ", " should ",
	}

	// 在文本前后加空格，确保词边界匹配
	padded := " " + text + " "
	for _, verb := range verbs {
		if strings.Contains(padded, verb) {
			return true
		}
	}

	return false
}
