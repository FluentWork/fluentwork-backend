package voicegateway

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

const (
	defaultVoiceHTTPAddr = ":8081"
	defaultAppServerURL  = "http://127.0.0.1:8080"
	// defaultIdleTimeout bounds each WebSocket read — a **half-open connection
	// detector**, not an idle-user reaper.
	//
	// The client sends an application-level ping every 30s (NAT keepalive and
	// liveness; every production WebSocket client does this). That resets this
	// deadline each time, so it can only expire when the client has *stopped
	// sending* — i.e. when it is gone. That is the job, and it does it.
	//
	// What it therefore cannot do is notice a **connected but idle user**: a
	// client that keeps pinging holds the session open, and with it the upstream
	// vendor duplex, until the user leaves. "Reap a user who stopped talking" is
	// a different capability, and it is **not implemented** — see `77_` P1-17.
	// Nobody should read this knob as if it were that.
	defaultIdleTimeout = 2 * time.Minute

	// defaultWriteTimeout bounds a single gateway→client write.
	//
	// Without it a client that stops reading fills its receive window, and the
	// write blocks **forever**. That is not one lost frame: the sink runs
	// synchronously on collectTurn's goroutine, so collectTurn stops calling
	// recv and the vendor duplex's send buffer fills behind it; and the read
	// loop blocks on the same `writeMu` the next time it has anything to send
	// — including the `interrupt` branch, which takes the lock even when the
	// provider returned nothing to write.
	//
	// Note what bounding a write costs here: `coder/websocket`'s
	// setupWriteTimeout closes the **whole connection** when the context
	// expires, not just the write. That is deliberate — speech is real time,
	// so a client that cannot take bytes for several seconds is not "slow", it
	// is gone, and dropping frames to keep a session alive would let it fall
	// further behind a stream it can never catch up to. Closing it lets
	// persistOnExit record the session and tears the vendor duplex down
	// cleanly, which is what a stalled client used to prevent.
	defaultWriteTimeout = 5 * time.Second
)

// Config holds voice-gateway process settings.
type Config struct {
	HTTPAddr             string
	AppEnv               string
	AppServerInternalURL string
	// RescueEnabled wires B8 stuck rescue into the gateway.
	//
	// It defaults to the environment's answer rather than to "on", because a
	// ladder the client cannot render is not a rescue: iOS has no rescue
	// handling yet, and the ladder is text-only until TTS is authorized
	// (P1-3/P2-4). Turning it on in production before then would record rescues
	// that never reached anyone — data that lies about the user's experience.
	// In development it is on, so the mechanism and its data can be exercised.
	RescueEnabled      bool
	InternalAPIToken   string
	Provider           string
	ClientAudioFormat  string
	VolcSpeechAPIKey   string
	VolcDuplexEndpoint string
	VolcDuplexModel    string
	VolcDuplexVoice    string
	// DevEchoText is the authoritative text the dev-echo provider returns
	// as ServerASRText on every user.speech.end. Only honored when
	// Provider == "dev-echo". Empty text makes the provider a no-op (logs
	// a warning on session open).
	DevEchoText string
	// DevEchoTTSMock, when true, makes the dev-echo provider emit frozen
	// WSS V2 ai.tts.start + 10 binary audio frames + ai.tts.end on each
	// user.speech.end. Used for the 9/13 cross-repo empty-run. Off by default.
	DevEchoTTSMock bool
	// DevEchoFixturePath is an optional 16 kHz mono s16le PCM (or WAV) file
	// streamed back after user.speech.end. Only honored when Provider is
	// dev-echo and DevEchoTTSMock is false. Empty means no audio fixture.
	DevEchoFixturePath string
	IdleTimeout        time.Duration
	// Rescue ladder timing (B8). Each falls back to its compiled-in default
	// when its variable is unset, so an environment that never sets them
	// behaves exactly as it did before they were configurable.
	//
	// They are configurable because the defaults were measured on one machine
	// on one day (see DefaultRescueSynthTimeout) while the ladder's latency is
	// dominated by two app-server round trips; tuning them against real
	// hardware should not need a rebuild. Validate refuses timings that break
	// the ladder's own invariants rather than clamping them the way
	// RescueThresholds.withDefaults does — an operator who asked for a ladder
	// that climbs backwards should be told, not quietly handed a different one.
	RescueLevel1After  time.Duration
	RescueLevel2After  time.Duration
	RescueLevel3After  time.Duration
	RescueGenTimeout   time.Duration
	RescueSynthTimeout time.Duration
	// loadErr carries a parse failure out of LoadConfig, which has no error
	// return of its own. Validate surfaces it first, so a malformed value fails
	// startup naming the variable instead of being silently replaced by its
	// default. A Config assembled by hand leaves it nil.
	loadErr error
}

// LoadConfig reads voice-gateway configuration from the environment.
// APP_ENV has no implicit default; local scripts must set it explicitly.
//
// Optional .env files are also loaded from the project root (if present):
//   - .env.dev      → base defaults for local development
//   - .env.volc.local → volcano/duobao credentials (gitignored, overrides .env.dev)
//
// Files are loaded in order; later files take precedence. Values exported
// to the environment by the shell still win over file values, so CI /
// container deployments don't need the dotenv files at all.
func LoadConfig() Config {
	root, _ := findProjectRoot()
	if root != "" {
		loadDotenvIfPresent(filepath.Join(root, ".env.dev"))
		loadDotenvIfPresent(filepath.Join(root, ".env.volc.local"))
	}

	appEnv := strings.TrimSpace(os.Getenv("APP_ENV"))
	token := strings.TrimSpace(os.Getenv("INTERNAL_API_TOKEN"))
	if token == "" && isDevelopmentEnv(appEnv) {
		token = config.DevInternalAPIToken
	}
	cfg := Config{
		HTTPAddr:             envOr("VOICE_GATEWAY_HTTP_ADDR", defaultVoiceHTTPAddr),
		AppEnv:               appEnv,
		AppServerInternalURL: envOr("APP_SERVER_INTERNAL_URL", defaultAppServerURL),
		RescueEnabled:        boolOr("VOICE_RESCUE_ENABLED", isDevelopmentEnv(appEnv)),
		InternalAPIToken:     token,
		Provider:             envOr("VOICE_GATEWAY_PROVIDER", "mock"),
		ClientAudioFormat:    envOr("VOICE_GATEWAY_CLIENT_AUDIO_FORMAT", "opus-framed"),
		DevEchoText:          envOr("VOICE_DEV_ECHO_TEXT", ""),
		DevEchoTTSMock: envTruthy("VOICE_DEV_ECHO_TTS_MOCK") ||
			envTruthy("DEV_ECHO_TTS_MOCK"),
		DevEchoFixturePath: envOr("VOICE_DEV_ECHO_FIXTURE", ""),
		VolcSpeechAPIKey: envFirst(
			"VOICE_GATEWAY_VOLC_SPEECH_API_KEY",
			"VOLC_POC_API_KEY",
			"VOLC_SPEECH_API_KEY",
			"VOLC_SPEECH_API_KEY_DEV",
		),
		VolcDuplexEndpoint: envFirst(
			"VOICE_GATEWAY_VOLC_DUPLEX_ENDPOINT",
			"VOLC_POC_ENDPOINT",
		),
		VolcDuplexModel: envFirst(
			"VOICE_GATEWAY_VOLC_DUPLEX_MODEL",
			"VOLC_DUPLEX_MODEL",
		),
		VolcDuplexVoice: envFirst(
			"VOICE_GATEWAY_VOLC_DUPLEX_VOICE",
			"VOLC_DUPLEX_VOICE",
		),
		IdleTimeout: durationOr("VOICE_GATEWAY_IDLE_TIMEOUT", defaultIdleTimeout),
	}
	cfg.loadErr = cfg.loadRescueTiming()
	return cfg
}

// loadRescueTiming fills the B8 ladder's timing knobs.
//
// A value that is unset keeps its compiled-in default. A value that is *set but
// unparseable* is reported as an error, not silently defaulted — see
// durationStrict for why the ladder's spacing does not get durationOr's
// silence.
func (c *Config) loadRescueTiming() error {
	knobs := []struct {
		key      string
		fallback time.Duration
		dst      *time.Duration
	}{
		{"VOICE_RESCUE_LEVEL1_AFTER", DefaultRescueLevel1After, &c.RescueLevel1After},
		{"VOICE_RESCUE_LEVEL2_AFTER", DefaultRescueLevel2After, &c.RescueLevel2After},
		{"VOICE_RESCUE_LEVEL3_AFTER", DefaultRescueLevel3After, &c.RescueLevel3After},
		{"VOICE_RESCUE_GEN_TIMEOUT", DefaultRescueGenTimeout, &c.RescueGenTimeout},
		{"VOICE_RESCUE_SYNTH_TIMEOUT", DefaultRescueSynthTimeout, &c.RescueSynthTimeout},
	}
	for _, knob := range knobs {
		value, err := durationStrict(knob.key, knob.fallback)
		if err != nil {
			return err
		}
		*knob.dst = value
	}
	return nil
}

// IsDevelopment reports whether the process is an explicitly configured local/test environment.
func (c Config) IsDevelopment() bool {
	return isDevelopmentEnv(c.AppEnv)
}

// Validate checks required gateway settings.
func (c Config) Validate() error {
	// A value that failed to parse outranks every rule below: reporting "must be
	// positive" about a number the operator never wrote would be worse than
	// useless. See Config.loadErr.
	if c.loadErr != nil {
		return c.loadErr
	}
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return fmt.Errorf("VOICE_GATEWAY_HTTP_ADDR is required")
	}
	if strings.TrimSpace(c.AppEnv) == "" {
		return fmt.Errorf("APP_ENV is required")
	}
	if strings.TrimSpace(c.AppServerInternalURL) == "" {
		return fmt.Errorf("APP_SERVER_INTERNAL_URL is required")
	}
	switch strings.ToLower(strings.TrimSpace(c.Provider)) {
	case "mock", "volc-duplex", "dev-echo":
	default:
		return fmt.Errorf("VOICE_GATEWAY_PROVIDER must be one of mock, volc-duplex, dev-echo")
	}
	switch strings.ToLower(strings.TrimSpace(c.ClientAudioFormat)) {
	case "opus-framed", "pcm-s16le":
	default:
		return fmt.Errorf("VOICE_GATEWAY_CLIENT_AUDIO_FORMAT must be one of opus-framed, pcm-s16le")
	}
	if strings.EqualFold(strings.TrimSpace(c.Provider), "volc-duplex") {
		if strings.TrimSpace(c.VolcSpeechAPIKey) == "" {
			return fmt.Errorf("VOICE_GATEWAY_PROVIDER=volc-duplex requires VOLC speech API key")
		}
	}
	if len(strings.TrimSpace(c.InternalAPIToken)) < 16 {
		return fmt.Errorf("INTERNAL_API_TOKEN must be at least 16 characters")
	}
	if !c.IsDevelopment() && (c.InternalAPIToken == "" || c.InternalAPIToken == config.DevInternalAPIToken) {
		return fmt.Errorf("INTERNAL_API_TOKEN must be set outside development environments")
	}
	if c.IdleTimeout <= 0 {
		return fmt.Errorf("VOICE_GATEWAY_IDLE_TIMEOUT must be positive")
	}
	return c.validateRescueTiming()
}

// validateRescueTiming refuses ladder timings that break the relationships the
// rescue code depends on.
//
// Each rule restates an invariant that a test already pins for the compiled-in
// defaults (rescue_client_test.go, and TestHTTPRescueSynthesizer_
// OwnBudgetIsInsideTheRungSpacing in internal/content/tts). Making the values
// configurable must not become a way around them, and unlike
// RescueThresholds.withDefaults this refuses rather than clamps: silently
// reordering a ladder someone configured backwards hides the mistake until the
// behaviour looks wrong for reasons nobody can see.
//
// A zero field is resolved to its default before the rules run, mirroring what
// the components themselves do with an unfilled Config. That keeps a
// hand-built Config validating against the timings it will actually run with,
// while a *set* zero — which reaches here only through LoadConfig, and which
// durationStrict has already refused — can never be quietly defaulted away.
func (c Config) validateRescueTiming() error {
	l1 := c.RescueLevel1After
	if l1 <= 0 {
		l1 = DefaultRescueLevel1After
	}
	l2 := c.RescueLevel2After
	if l2 <= 0 {
		l2 = DefaultRescueLevel2After
	}
	l3 := c.RescueLevel3After
	if l3 <= 0 {
		l3 = DefaultRescueLevel3After
	}
	synthTimeout := c.RescueSynthTimeout
	if synthTimeout <= 0 {
		synthTimeout = DefaultRescueSynthTimeout
	}

	if l2 < l1 {
		return fmt.Errorf("VOICE_RESCUE_LEVEL2_AFTER (%s) must be at least VOICE_RESCUE_LEVEL1_AFTER (%s)", l2, l1)
	}
	if l3 < l2 {
		return fmt.Errorf("VOICE_RESCUE_LEVEL3_AFTER (%s) must be at least VOICE_RESCUE_LEVEL2_AFTER (%s)", l3, l2)
	}
	// The synthesizer's own timeout is the *effective* bound on the rung's
	// audio: the orchestrator hands Synthesize the outer context, which carries
	// no deadline, so the client's timeout is what actually applies (see
	// HTTPRescueSynthesizer.Synthesize). Audio that outlives the rung spacing
	// lands after the next rung is due — the failure this bound exists to stop.
	if synthTimeout >= l1 {
		return fmt.Errorf("VOICE_RESCUE_SYNTH_TIMEOUT (%s) must stay under VOICE_RESCUE_LEVEL1_AFTER (%s)", synthTimeout, l1)
	}

	// RescueGenTimeout is deliberately exempt from the same rule, because it is
	// not an effective bound: generateText wraps every call in a context
	// bounded by the rung budget, so generation is capped at
	// min(RescueGenTimeout, Level1) and the context wins whenever it is the
	// smaller. Refusing RescueGenTimeout >= Level1 would make shortening the
	// rung spacing alone impossible — 2s rungs would demand a sub-2s generator
	// timeout that changes nothing. Its malformed-value handling is unaffected:
	// loadRescueTiming still parses it, so a bad value fails startup.
	return nil
}

// ApplyDevEchoFixtureFlag overlays a CLI `--dev-echo-fixture` path. Empty
// leaves the env-derived DevEchoFixturePath unchanged.
func (c *Config) ApplyDevEchoFixtureFlag(path string) {
	if p := strings.TrimSpace(path); p != "" {
		c.DevEchoFixturePath = p
	}
}

func envTruthy(key string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(key))) {
	case "1", "true", "yes", "on":
		return true
	default:
		return false
	}
}

// boolOr reads a boolean setting; anything unparseable keeps the fallback.
func boolOr(key string, fallback bool) bool {
	raw := strings.TrimSpace(strings.ToLower(os.Getenv(key)))
	switch raw {
	case "":
		return fallback
	case "1", "true", "yes", "on":
		return true
	case "0", "false", "no", "off":
		return false
	default:
		return fallback
	}
}

func envOr(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

// durationStrict is durationOr except that a value which is set and does not
// parse becomes an error instead of a silent fallback.
//
// durationOr's silence is defensible where the fallback is also a defensible
// answer. It is not defensible for the ladder's spacing: an operator who writes
// VOICE_RESCUE_LEVEL1_AFTER=5 meaning five seconds, and gets three, has no way
// to find out — the same class of lie as a pricing file that fails to parse and
// leaves yesterday's rates in place (see ARK_PRICING_FILE in CLAUDE.md).
func durationStrict(key string, fallback time.Duration) (time.Duration, error) {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback, nil
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return 0, fmt.Errorf("%s=%q is not a duration (want a Go duration such as 3s or 2500ms)", key, raw)
	}
	// A non-positive value is refused here rather than in Validate because this
	// is the only place that knows the variable was *set*. Downstream, zero is
	// indistinguishable from "field never filled" — which has a legitimate
	// meaning (a hand-built Config, resolved to the defaults) and must not be
	// conflated with an operator asking for a zero-second rung.
	if parsed <= 0 {
		return 0, fmt.Errorf("%s=%q must be positive", key, raw)
	}
	return parsed, nil
}

func durationOr(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

func envFirst(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
	}
	return ""
}

func isDevelopmentEnv(appEnv string) bool {
	switch strings.ToLower(strings.TrimSpace(appEnv)) {
	case "development", "dev", "test", "local":
		return true
	default:
		return false
	}
}

// findProjectRoot walks upward from the current working directory looking
// for the FluentWork backend module root (the directory containing go.mod).
// Returns "" when not found so callers can degrade gracefully.
func findProjectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			// Extra sanity check: voice-gateway should live here.
			if _, err := os.Stat(filepath.Join(dir, "internal", "voicegateway")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// loadDotenvIfPresent reads KEY=VALUE lines from path into the process
// environment, but only when the variable is not already set. This lets
// real environment variables (CI / shell exports) win over file values,
// while still allowing a local .env.volc.local to seed defaults.
func loadDotenvIfPresent(path string) {
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return // file missing / unreadable is non-fatal
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		value := strings.TrimSpace(line[eq+1:])
		// Strip surrounding quotes (single or double) if present.
		if len(value) >= 2 {
			first, last := value[0], value[len(value)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if _, already := os.LookupEnv(key); already {
			continue
		}
		_ = os.Setenv(key, value)
	}
}
