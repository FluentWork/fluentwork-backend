// Command asr-wer measures ASR word error rate on the diagnostic sample set.
//
// This is meta 77_ plan step 5, the measurement that decides whether the voice
// path stays on end-to-end duplex or moves to a split pipeline.
//
// Usage:
//
//	./scripts/asr-wer.sh --audio-dir path/to/wavs
//	./scripts/asr-wer.sh --audio-dir path/to/wavs --json out.json
//	./scripts/asr-wer.sh --audio-dir path/to/wavs --only wer-01,wer-02
//
// Audio is `<id>.wav`, 16 kHz mono PCM16 — see the fixture for the ids.
//
// **What the number means depends entirely on the audio.** Samples recorded by a
// Chinese speaker reading the reference lines measure the thing this step exists
// to measure. Synthetic audio (say/Kokoro, however the voice is chosen) measures
// how the ASR handles *native* English and says nothing about accent. The report
// prints which of the two it is holding, because a number like this gets quoted
// without its provenance and then used to make a decision.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "asr-wer:", err)
		os.Exit(1)
	}
}

type sampleResult struct {
	ID         string   `json:"id"`
	Scene      string   `json:"scene"`
	Difficulty string   `json:"difficulty"`
	Traps      []string `json:"traps"`
	Reference  string   `json:"reference"`
	Hypothesis string   `json:"hypothesis"`
	voicepoc.WERResult
	Rate float64 `json:"rate"`
	// Normalised is the same utterance scored after NormalizeSurface. Carried
	// alongside rather than instead of the raw result: the gap between them is
	// the measurement of how much of the "error" was spelling.
	Normalised     voicepoc.WERResult `json:"normalised"`
	NormalisedRate float64            `json:"normalised_rate"`
}

func run() error {
	audioDir := flag.String("audio-dir", "", "directory of <id>.wav files (16 kHz mono PCM16)")
	samplesPath := flag.String("samples", "", "sample fixture file (default: the set embedded in the binary)")
	outPath := flag.String("json", "", "also write the full per-sample report here")
	only := flag.String("only", "", "comma-separated sample ids to run")
	flag.Parse()

	if strings.TrimSpace(*audioDir) == "" {
		return fmt.Errorf("--audio-dir is required")
	}
	var (
		set voicepoc.WERSampleSet
		err error
	)
	if *samplesPath == "" {
		set, err = voicepoc.ShippedWERSamples()
	} else {
		set, err = voicepoc.LoadWERSamples(*samplesPath)
	}
	if err != nil {
		return err
	}

	selected := set.Samples
	if *only != "" {
		want := map[string]bool{}
		for _, id := range strings.Split(*only, ",") {
			want[strings.TrimSpace(id)] = true
		}
		selected = nil
		for _, s := range set.Samples {
			if want[s.ID] {
				selected = append(selected, s)
			}
		}
		if len(selected) == 0 {
			return fmt.Errorf("--only matched none of the %d samples", len(set.Samples))
		}
	}

	cfg, err := duplexConfigFromEnv()
	if err != nil {
		return err
	}

	// A missing WAV is reported and counted, never skipped silently: a run that
	// quietly measures 12 of 30 samples reports a number for a set it did not
	// run.
	var (
		results []sampleResult
		missing []string
		failed  []string
	)

	for _, s := range selected {
		wavPath := filepath.Join(*audioDir, s.AudioFileName())
		if _, statErr := os.Stat(wavPath); statErr != nil {
			missing = append(missing, s.ID)
			continue
		}

		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		hypothesis, txErr := voicepoc.TranscribeFixture(ctx, cfg, wavPath)
		cancel()
		if txErr != nil {
			failed = append(failed, s.ID)
			fmt.Fprintf(os.Stderr, "  %s: %v\n", s.ID, txErr)
			continue
		}

		scored := voicepoc.ScoreWER(s.Text, hypothesis)
		normalised := voicepoc.ScoreWERNormalized(s.Text, hypothesis)
		results = append(results, sampleResult{
			ID: s.ID, Scene: s.Scene, Difficulty: s.Difficulty, Traps: s.Traps,
			Reference: s.Text, Hypothesis: strings.TrimSpace(hypothesis),
			WERResult: scored, Rate: scored.Rate(),
			Normalised: normalised, NormalisedRate: normalised.Rate(),
		})
		fmt.Printf("  %-8s %5.1f%% (归一化 %4.1f%%)  said: %s\n",
			s.ID, scored.Rate()*100, normalised.Rate()*100, strings.TrimSpace(hypothesis))
	}

	if len(results) == 0 {
		return fmt.Errorf("no sample produced a transcript (missing %d, failed %d)", len(missing), len(failed))
	}

	report := buildReport(results, missing, failed)
	printReport(report)

	if *outPath != "" {
		raw, _ := json.MarshalIndent(map[string]any{
			"samples": results,
			"report":  report,
		}, "", "  ")
		if err := os.WriteFile(*outPath, raw, 0o644); err != nil {
			return err
		}
		fmt.Printf("\nper-sample report → %s\n", *outPath)
	}
	return nil
}

type slice struct {
	Key      string  `json:"key"`
	Samples  int     `json:"samples"`
	Rate     float64 `json:"rate"`
	Subs     int     `json:"substitutions"`
	Dels     int     `json:"deletions"`
	Inserts  int     `json:"insertions"`
	RefWords int     `json:"reference_words"`
}

type report struct {
	SamplesRun     int     `json:"samples_run"`
	SamplesMissing int     `json:"samples_missing"`
	SamplesFailed  int     `json:"samples_failed"`
	OverallRate    float64 `json:"overall_rate"`
	// The same run, scored after normalisation. Both are reported because
	// neither is the truth on its own: the raw number carries formatting
	// differences the recogniser had no part in, and the normalised one carries
	// whatever the rules happen to admit. The gap between them is the part
	// worth arguing about.
	OverallNormalisedRate float64 `json:"overall_normalised_rate"`
	// FormattingOnlyErrors is raw errors minus normalised errors — the count of
	// mistakes that were spelling rather than mishearing.
	FormattingOnlyErrors int     `json:"formatting_only_errors"`
	ByTrap               []slice `json:"by_trap"`
	ByScene              []slice `json:"by_scene"`
	ByDifficulty         []slice `json:"by_difficulty"`
}

func buildReport(results []sampleResult, missing, failed []string) report {
	rep := report{
		SamplesRun:     len(results),
		SamplesMissing: len(missing),
		SamplesFailed:  len(failed),
	}
	total := voicepoc.WERResult{}
	for _, r := range results {
		total.Substitutions += r.Substitutions
		total.Deletions += r.Deletions
		total.Insertions += r.Insertions
		total.Correct += r.Correct
		total.ReferenceLength += r.ReferenceLength
	}
	rep.OverallRate = total.Rate()

	normalisedTotal := voicepoc.WERResult{}
	for _, r := range results {
		normalisedTotal.Substitutions += r.Normalised.Substitutions
		normalisedTotal.Deletions += r.Normalised.Deletions
		normalisedTotal.Insertions += r.Normalised.Insertions
		normalisedTotal.ReferenceLength += r.Normalised.ReferenceLength
	}
	rep.OverallNormalisedRate = normalisedTotal.Rate()
	rep.FormattingOnlyErrors = total.Errors() - normalisedTotal.Errors()

	byTrap := map[string][]sampleResult{}
	for _, r := range results {
		for _, trap := range r.Traps {
			byTrap[trap] = append(byTrap[trap], r)
		}
	}
	rep.ByTrap = summarize(byTrap)
	rep.ByScene = summarize(groupBy(results, func(r sampleResult) string { return r.Scene }))
	rep.ByDifficulty = summarize(groupBy(results, func(r sampleResult) string { return r.Difficulty }))
	return rep
}

func groupBy(results []sampleResult, key func(sampleResult) string) map[string][]sampleResult {
	out := map[string][]sampleResult{}
	for _, r := range results {
		k := key(r)
		out[k] = append(out[k], r)
	}
	return out
}

// summarize sums the *counts* before dividing, rather than averaging per-sample
// rates. Averaging rates weights a five-word sample the same as a fifteen-word
// one, which lets the short easy lines carry the number.
func summarize(groups map[string][]sampleResult) []slice {
	out := make([]slice, 0, len(groups))
	for key, group := range groups {
		total := voicepoc.WERResult{}
		for _, r := range group {
			total.Substitutions += r.Substitutions
			total.Deletions += r.Deletions
			total.Insertions += r.Insertions
			total.ReferenceLength += r.ReferenceLength
		}
		out = append(out, slice{
			Key: key, Samples: len(group), Rate: total.Rate(),
			Subs: total.Substitutions, Dels: total.Deletions,
			Inserts: total.Insertions, RefWords: total.ReferenceLength,
		})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Rate > out[j].Rate })
	return out
}

func printReport(rep report) {
	fmt.Printf("\n=== ASR WER · %d/%d samples", rep.SamplesRun, rep.SamplesRun+rep.SamplesMissing+rep.SamplesFailed)
	if rep.SamplesMissing > 0 {
		fmt.Printf(" (%d missing audio", rep.SamplesMissing)
		if rep.SamplesFailed > 0 {
			fmt.Printf(", %d failed", rep.SamplesFailed)
		}
		fmt.Printf(")")
	} else if rep.SamplesFailed > 0 {
		fmt.Printf(" (%d failed)", rep.SamplesFailed)
	}
	fmt.Printf(" ===\n\n")
	fmt.Printf("总体 WER: %.1f%%\n", rep.OverallRate*100)
	fmt.Printf("          归一化 %.1f%%  (两者相差 %d 处, 是写法差异不是听错)\n",
		rep.OverallNormalisedRate*100, rep.FormattingOnlyErrors)
	fmt.Printf("\n两个数都要报: 原始数里混着识别器没有参与的写法差异, 归一化数里混着\n")
	fmt.Printf("这套规则恰好承认的东西。**相差的那部分才是值得争论的。**\n")

	printSlice("按音素陷阱(这是这个样本集存在的意义)", rep.ByTrap)
	printSlice("按场景", rep.ByScene)
	printSlice("按难度", rep.ByDifficulty)

	fmt.Printf("\nS = 替换(听成了别的词) · D = 删除(没听见) · I = 插入(凭空多出)\n")
	fmt.Printf("\n数字的含义取决于音频来源。真人中式口音 = 这一步要测的东西;\n")
	fmt.Printf("合成音频 = ASR 在标准英语上的表现, 与口音无关, 不能用来决定架构。\n")
}

func printSlice(title string, rows []slice) {
	if len(rows) == 0 {
		return
	}
	fmt.Printf("\n%s\n", title)
	fmt.Printf("  %-16s %5s %7s %5s %5s %5s\n", "", "n", "WER", "S", "D", "I")
	for _, row := range rows {
		fmt.Printf("  %-16s %5d %6.1f%% %5d %5d %5d\n", row.Key, row.Samples, row.Rate*100, row.Subs, row.Dels, row.Inserts)
	}
}

func duplexConfigFromEnv() (voicepoc.DuplexConfig, error) {
	apiKey := firstNonEmpty(
		os.Getenv("VOLC_POC_API_KEY"),
		os.Getenv("VOLC_SPEECH_API_KEY"),
		os.Getenv("VOLC_SPEECH_API_KEY_DEV"),
	)
	if apiKey == "" {
		return voicepoc.DuplexConfig{}, fmt.Errorf("VOLC_SPEECH_API_KEY is empty; fill .env.volc.local")
	}
	return voicepoc.DuplexConfig{
		APIKey:   apiKey,
		Endpoint: strings.TrimSpace(os.Getenv("VOLC_POC_ENDPOINT")),
		Model:    firstNonEmpty(os.Getenv("VOLC_DUPLEX_MODEL"), "1.2.6.0"),
		Voice:    firstNonEmpty(os.Getenv("VOLC_DUPLEX_VOICE"), "zh_female_vv_jupiter_bigtts"),
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
