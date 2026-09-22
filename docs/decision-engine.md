# Structured decision engine

`internal/decision` is the boundary for optional, typed local decisions. It is
separate from `internal/provider`: providers generate conversational text,
while a decision engine returns bounded answers such as a choice, ordinal
score, or probability. The existing agent, verifier, approval policy, and
generative fallback remain authoritative.

The first implementation slice adds three pieces:

1. `decision.Engine`, `Question`, `Result`, and validation contracts;
2. a Hugging Face model manager for the official Laya source checkpoints;
3. `llmtui decision models|pull|inspect|verify` for explicit acquisition and
   integrity inspection.

The model manager selects only the checkpoint files required by Laya's current
Python loader: `model.safetensors`, `rl_agent_config.json`, `tokenizer/**`, and
`encoder/**`. It resolves a branch or tag to a commit SHA, sends an optional
`HF_TOKEN` only as an HTTP authorization header, resumes an existing partial
file when the server honors a byte range, verifies size and SHA-256 when
available, writes a manifest, and publishes the completed revision by atomic
rename. Incomplete staging data has no final manifest and is never treated as
installed.

The manifest explicitly separates the upstream source checkpoint from the
runtime representation. Current Laya upstream publishes SafeTensors and its
custom decision head, but this repository has not yet established a verified
ONNX export or a compatible Go ONNX runtime artifact. Consequently a source
installation reports `runtime.ready: false` and the generic fallback engine
returns `ErrUnavailable`; it never fabricates answers and never starts Python
or performs network inference. The next implementation slice must establish
Python-vs-runtime golden fixtures and a reproducible, checksum-pinned runtime
artifact before enabling predictions.

The second slice now makes that boundary executable in code. A ready runtime
manifest must identify an ONNX file, its size and SHA-256, the exporter, and
the upstream revision used to create it. `LoadRuntime` revalidates the
manifest, rejects source-only checkpoints, rejects symlinked or tampered
artifacts, and calls a narrow `RuntimeLoader` only after those checks pass.
The loader is deliberately injected so a native backend can be selected after
its platform and licensing requirements are verified. `GoldenFixture` provides
the companion parity contract: pinned structured inputs and normalized answers
can be produced by a Python reference run and consumed by a future Go runtime
without treating token IDs as a public API.

The sequence builder is the third slice. `BuildSequence` reproduces the
checkpoint's bounded layout and marker bookkeeping behind a small
`TokenEncoder` interface: typed question head, one mask marker per option,
bounded option text, separator tokens, and bounded state text with explicit
left or right truncation. Choice maps are sorted in Go because their source
type has no insertion order; callers that require checkpoint order should use
an ordered choice list and parity fixtures should cover that choice explicitly.

The fourth slice adds `DecodeLogits`, which applies the same bounded
temperature buckets and entropy confidence calculation used by the reference
agent, then produces normalized choice, expected score, or noul answers. It
uses stable softmax and rejects non-finite logits before any result reaches a
caller. Calibration values outside the usable range are clamped and therefore
cannot sharpen an uncertain result into a false certainty.

Model acquisition is explicit (`llmtui decision pull laya:english`); normal
startup and normal chat do not contact Hugging Face. The feature is disabled
by default in `decision_engine.enabled` and is not wired into agent policy yet.
