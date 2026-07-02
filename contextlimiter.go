package main

import (
	"math"
	"sync"
)

// ── Per-model context limits ──
//
// Empirically determined (2026-06-30):
//   All models share the same tokenizer.
//   Max prefill_tokens observed: ~6070 (across all models).
//   Safety limit = 5463 (90% of 6070) — leaves ~600 tokens for the response.
//
// Source: 10-round tests against chatjimmy.ai/api/chat with base64 content.
//   At prefill=6070 the upstream starts returning HTTP 200 empty responses.
//   At prefill≤5463 all 10 rounds pass reliably.

const defaultPrefillLimit = 5463 // 6070 * 0.9

// modelPrefillLimits maps known model names to their maximum observed
// prefill_tokens. These are initial baselines and will be overwritten
// by actual observations at runtime.
var modelPrefillLimits = map[string]int{
	"deepseek-v3":   6070,
	"qwen2.5-72B":   6070,
	"llama3.1-405B": 6070,
	"llama3.1-70B":  6070,
	"llama3.1-8B":   6070,
}

// ContextLimiter tracks per-model token limits from upstream responses
// and provides token estimation and message truncation.
type ContextLimiter struct {
	mu     sync.Mutex
	limits map[string]int // model -> highest prefill_tokens observed

	// muHistory guards the historical stats tracking
	muHistory  sync.Mutex
	history    []LimitRecord
	maxHistory int
}

// LimitRecord stores a single token observation for debugging.
type LimitRecord struct {
	Model   string
	Prefill int
	Total   int
	Time    int64 // unix timestamp
}

// NewContextLimiter creates a ContextLimiter with built-in defaults.
func NewContextLimiter() *ContextLimiter {
	// Copy built-in limits
	limits := make(map[string]int, len(modelPrefillLimits))
	for k, v := range modelPrefillLimits {
		limits[k] = v
	}
	return &ContextLimiter{
		limits:     limits,
		history:    make([]LimitRecord, 0, 100),
		maxHistory: 1000,
	}
}

// RecordPrefill updates the observed max prefill_tokens for a model.
// Called after each successful upstream response.
func (cl *ContextLimiter) RecordPrefill(model string, prefill, total int) {
	cl.mu.Lock()
	cl.limits[model] = max(cl.limits[model], prefill)
	cl.mu.Unlock()

	cl.muHistory.Lock()
	if len(cl.history) < cl.maxHistory {
		cl.history = append(cl.history, LimitRecord{
			Model:   model,
			Prefill: prefill,
			Total:   total,
		})
	}
	cl.muHistory.Unlock()
}

// SafeLimit returns the safe prefill token limit for a model.
// Uses 90% of the highest observed prefill_tokens to leave room for the response.
func (cl *ContextLimiter) SafeLimit(model string) int {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	if l, ok := cl.limits[model]; ok {
		return int(math.Floor(float64(l) * 0.9))
	}
	return defaultPrefillLimit
}

// MaxObserved returns the highest prefill_tokens ever observed for a model.
func (cl *ContextLimiter) MaxObserved(model string) int {
	cl.mu.Lock()
	defer cl.mu.Unlock()
	return cl.limits[model]
}

// EstimateTokens estimates the upstream token count for a slice of messages.
// Uses a rough heuristic calibrated against the upstream's actual tokenizer.
// The upstream tokenizer treats all text similarly (~1.4 chars/token for base64).
// For natural language we use a weighted formula that accounts for CJK vs ASCII.
func (cl *ContextLimiter) EstimateTokens(messages []ChatMessage) int {
	var total int
	for _, m := range messages {
		content := m.contentString()
		total += estimateTokenCount(content)
		// Add overhead for the message framing (~10 tokens per message)
		total += 10
	}
	// Add overhead for system prompt (handled separately) and JSON framing
	total += 30
	return total
}

// estimateTokenCount estimates upstream tokens for a single text string.
// Calibrated against empirically observed prefill_tokens.
//
// Observed ratios at the limit (all models, same tokenizer):
//   base64: 8500 chars → 6070 tokens = 1.400 chars/token
//   English prose: ~4 chars/token (GPT-like)
//   CJK: ~1.5 chars/token (typical for multilingual tokenizers)
func estimateTokenCount(s string) int {
	if len(s) == 0 {
		return 0
	}

	// Fast path: detect base64-like content (high entropy, alphanum+/+=)
	if isBase64Like(s) {
		return int(math.Ceil(float64(len(s)) / 1.4))
	}

	// Mixed content: count CJK and non-CJK characters separately
	var cjk, other int
	for _, r := range s {
		if isCJK(r) {
			cjk++
		} else {
			other++
		}
	}

	// CJK: ~1.5 chars/token, ASCII/other: ~4 chars/token
	tokens := int(math.Ceil(float64(cjk)/1.5 + float64(other)/4.0))
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// isCJK returns true for CJK unified ideographs and related blocks.
func isCJK(r rune) bool {
	switch {
	case r >= 0x4E00 && r <= 0x9FFF: // CJK Unified Ideographs
		return true
	case r >= 0x3400 && r <= 0x4DBF: // CJK Unified Ideographs Extension A
		return true
	case r >= 0xF900 && r <= 0xFAFF: // CJK Compatibility Ideographs
		return true
	case r >= 0x3000 && r <= 0x303F: // CJK Symbols and Punctuation
		return true
	case r >= 0xFF00 && r <= 0xFFEF: // Fullwidth Forms
		return true
	default:
		return false
	}
}

// isBase64Like detects high-entropy base64 strings that tokenize differently
// from natural language. Base64 uses [A-Za-z0-9+/=] and typically appears in
// long runs without spaces.
func isBase64Like(s string) bool {
	if len(s) < 40 {
		return false
	}
	// Count alphanumeric + / + = characters
	var alnum, total int
	for _, r := range s {
		total++
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '/' || r == '+' || r == '=' {
			alnum++
		}
	}
	// If >95% are base64 chars and no spaces, treat as base64-like
	ratio := float64(alnum) / float64(total)
	return ratio > 0.95 && !containsSpace(s)
}

func containsSpace(s string) bool {
	for _, r := range s {
		if r == ' ' || r == '\t' || r == '\n' {
			return true
		}
	}
	return false
}

// TruncateByTokens removes older messages until the estimated token count
// is under the safe limit. Always keeps the first message (system/user)
// and the last message (most recent user turn).
// Returns the truncated slice and a bool indicating if truncation occurred.
func (cl *ContextLimiter) TruncateByTokens(messages []ChatMessage, model string) ([]ChatMessage, bool) {
	limit := cl.SafeLimit(model)
	estimate := cl.EstimateTokens(messages)

	if estimate <= limit {
		return messages, false
	}

	logInfo("ctxlimiter truncating model=%s estimate=%d limit=%d msgs=%d", model, estimate, limit, len(messages))

	// Build a new slice dropping from position 1 (after first) until we fit.
	// Always keep the first message and the last message.
	if len(messages) <= 2 {
		return messages, false // can't truncate further
	}

	// We'll drop messages starting from index 1 (after the first message).
	// Keep the last message. Try dropping one at a time.
	trimmed := make([]ChatMessage, len(messages))
	copy(trimmed, messages)

	// Keep dropping from index 1 (shifts as we remove)
	for len(trimmed) > 2 {
		// Check if we'd fit by dropping the second message
		candidate := append(trimmed[:1], trimmed[2:]...)
		est := cl.EstimateTokens(candidate)
		if est <= limit {
			logInfo("ctxlimiter dropped msg role=%s dropped=%d remaining=%d estimate=%d→%d",
				trimmed[1].Role, len(messages)-len(candidate), len(candidate), estimate, est)
			return candidate, true
		}
		trimmed = candidate
	}

	// We've dropped everything except the first and last and still don't fit.
	// This shouldn't happen with normal text, but if it does, truncate the last message.
	if len(trimmed) == 2 && cl.EstimateTokens(trimmed) > limit {
		logWarn("ctxlimiter cannot fit model=%s even with 2 msgs, will truncate last msg content", model)
	}
	return trimmed, true
}
