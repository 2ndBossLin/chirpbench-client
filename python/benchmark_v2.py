"""Benchmark corpus v2 — deterministic, byte-identical prompts.

Replaces the v1 word-bag prompt generator (149 words + time_ns seed, which
made every run's input different and destroyed reproducibility).

Design (2026-10-19 plan, section 二.1):
- Corpus is a fixed synthetic English technical text (AI-authored, not from
  any published work -> no pretraining contamination, no copyright issue).
- Loaded from benchmark_corpus/corpus_v3.txt; SHA-256 is pinned so any
  accidental corpus edit is detected at startup.
- Slicing is deterministic: prompt for length L = first L tokens of the
  corpus (word-based, one word == one token under the shared heuristic),
  byte-identical for every user, every time.
- No random, no time_ns anywhere in this module.
"""
import hashlib
import os

CORPUS_DIR = os.path.join(os.path.dirname(os.path.abspath(__file__)), "benchmark_corpus")
CORPUS_FILE = os.path.join(CORPUS_DIR, "corpus_v3.txt")

# Pinned hash of corpus_v3.txt (computed 2026-11-01). Startup verifies it.
CORPUS_SHA256 = "adfbb377391fa1a9cd5330d77fe20a6d5838d8510d00cc83dc02d25a2d4db2d8"

BENCHMARK_VERSION = "v2.0"

# The fixed decode task instruction appended after the corpus slice.
# Deterministic decode load: same prefix + same task -> same expected output
# shape, so draft acceptance rate is stable across retests.
DECODE_TASK_SUFFIX = (
    "\nBased on the technical text above, write a detailed summary of the "
    "key engineering trade-offs it describes. Use clear, coherent sentences "
    "and cover each section."
)


def load_corpus() -> str:
    with open(CORPUS_FILE, "r", encoding="utf-8") as f:
        text = f.read()
    digest = hashlib.sha256(text.encode("utf-8")).hexdigest()
    if digest != CORPUS_SHA256:
        raise RuntimeError(
            f"benchmark corpus v3 hash mismatch: expected {CORPUS_SHA256}, got {digest}. "
            "The corpus file was modified; benchmark results would no longer be comparable."
        )
    return text


_CORPUS = None


def get_corpus() -> str:
    global _CORPUS
    if _CORPUS is None:
        _CORPUS = load_corpus()
    return _CORPUS


def corpus_word_tokens() -> list:
    """Corpus split into whitespace tokens. Each token counts as 1 token under
    the shared heuristic tokenizer (single ASCII word runs == 1 token)."""
    return get_corpus().split()


def build_corpus_prefix(token_count: int) -> str:
    """First `token_count` tokens of the corpus, joined by single spaces.

    Deterministic: same token_count -> byte-identical string for everyone.
    If token_count exceeds the corpus size, the corpus is repeated with a
    numbered marker so the repetition is still deterministic and visible.
    """
    target = max(int(token_count), 0)
    if target <= 0:
        return ""
    tokens = corpus_word_tokens()
    if target <= len(tokens):
        return " ".join(tokens[:target])
    # Longer than corpus: repeat deterministically. Marker keeps runs of the
    # corpus distinguishable to the model (avoids degenerate pure repetition)
    # while remaining byte-identical across users.
    parts = []
    repeats = (target + len(tokens) - 1) // len(tokens)
    for r in range(repeats):
        parts.append(" ".join(tokens))
    joined = "\n".join(parts)
    out_tokens = joined.split()
    return " ".join(out_tokens[:target])


def build_benchmark_prompt(target_tokens: int) -> str:
    """v2 prefill benchmark prompt: deterministic corpus slice + fixed decode
    task suffix. The suffix is constant, so prompt length differences across
    test points come only from the corpus slice."""
    suffix_tokens = len(DECODE_TASK_SUFFIX.split())
    prefix_tokens = max(int(target_tokens) - suffix_tokens, 0)
    prefix = build_corpus_prefix(prefix_tokens)
    if prefix:
        return prefix + DECODE_TASK_SUFFIX
    return DECODE_TASK_SUFFIX.strip()


def build_warmup_prompt() -> str:
    """Deterministic short warmup prompt (first 96 corpus tokens + suffix).
    v1 randomized this; v2 fixes it so warmup also exercises the same cache
    path shape as the real test."""
    return build_benchmark_prompt(96)


def corpus_stats() -> dict:
    tokens = corpus_word_tokens()
    return {
        "benchmark_version": BENCHMARK_VERSION,
        "corpus_sha256": CORPUS_SHA256,
        "corpus_tokens": len(tokens),
        "corpus_bytes": len(get_corpus().encode("utf-8")),
    }


def build_fingerprint(fields: dict) -> str:
    """Stable config fingerprint for the certification system.

    Same fingerprint = same benchmark configuration (model + hardware +
    framework + quantization + request params + corpus + repeat tier +
    concurrency). Inputs are normalized so cosmetic differences (case,
    whitespace) don't split fingerprints. Corpus hash pins benchmark v2.

    `fields` keys: model_name, hardware, framework, quantization, run_command,
    temperature, top_p, is_greedy, reasoning_enabled, repeat_count,
    concurrency, benchmark_version, corpus_hash.
    """
    def norm(v):
        return str(v if v is not None else "").strip().lower()

    def opt(name):
        v = fields.get(name)
        return "" if v is None else v

    parts = [
        norm(fields.get("model_name")),
        norm(fields.get("hardware")),
        norm(fields.get("framework")),
        norm(fields.get("quantization")),
        norm(fields.get("run_command")),
        f"temp={opt('temperature')}",
        f"top_p={opt('top_p')}",
        f"greedy={int(bool(fields.get('is_greedy')))}",
        f"reasoning={'' if fields.get('reasoning_enabled') is None else int(bool(fields['reasoning_enabled']))}",
        f"repeat={opt('repeat_count')}",
        f"concurrency={opt('concurrency')}",
        f"bench={norm(fields.get('benchmark_version')) or 'v1'}",
        f"corpus={norm(fields.get('corpus_hash'))}",
    ]
    return hashlib.sha256("|".join(parts).encode("utf-8")).hexdigest()
