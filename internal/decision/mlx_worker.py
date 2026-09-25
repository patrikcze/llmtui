"""Private, versioned stdio worker. No downloads and no model routing here."""
import json
import os
import platform
import sys
import time
from pathlib import Path

# Keep even native libraries' stdout out of the protocol stream.
protocol_out = os.fdopen(os.dup(1), "w", buffering=1)
os.dup2(2, 1)
sys.stdout = sys.stderr
os.environ["HF_HUB_OFFLINE"] = "1"
os.environ["HF_HUB_DISABLE_TELEMETRY"] = "1"
# Protocol 2 adds the "strict" predict request field and the "input_usage"
# result field (token-capacity diagnostics, see _measure_inputs below).
PROTOCOL = 2
MAX_LINE = 8 * 1024 * 1024


def send(kind, request_id=0, **fields):
    protocol_out.write(json.dumps(dict(type=kind, protocol=PROTOCOL, id=request_id, **fields), allow_nan=False) + "\n")


def receive():
    line = sys.stdin.buffer.readline(MAX_LINE + 1)
    if not line:
        raise EOFError()
    if len(line) > MAX_LINE or not line.endswith(b"\n"):
        raise ValueError("invalid frame")
    msg = json.loads(line)
    if msg.get("protocol") != PROTOCOL:
        raise ValueError("unsupported protocol")
    return msg


def main():
    init = receive()
    if init.get("type") not in ("init", "probe"):
        send("error", code="protocol")
        return
    if sys.version_info < (3, 11) or sys.platform != "darwin" or platform.machine() != "arm64":
        send("error", code="platform")
        return
    started = time.perf_counter()
    try:
        from importlib.metadata import version
        import laya_mlx as laya
        from laya_mlx.common import build_prefix, render_options, serialize_state
        import mlx.core as mx
        package_version = version("laya-mlx")
        if package_version != "0.2.0":
            send("error", code="version")
            return
        if not mx.metal.is_available():
            send("error", code="metal")
            return
    except ImportError:
        send("error", code="dependencies")
        return
    import_ms = (time.perf_counter() - started) * 1000
    info = dict(version=package_version, python=platform.python_version(), import_ms=import_ms)
    if init["type"] == "probe":
        send("ready", **info)
        return
    path = Path(init["model_path"])
    if not path.is_absolute() or not path.is_dir():
        send("error", code="model_path")
        return
    started = time.perf_counter()
    try:
        # Absolute Path forces the local-only branch in laya-mlx resolve_model.
        agent = laya.load(path, dtype="float16", device="gpu", batch_size=16,
                          compile=False, cache_prompts=False)
    except Exception:
        send("error", code="load")
        return
    send("ready", load_ms=(time.perf_counter() - started) * 1000, **info)

    def measure_inputs(state, questions):
        """Per-question token-capacity diagnostics computed with the exact
        pinned tokenizer (agent.tok) and the exact upstream sequence-
        construction rules (laya_mlx.common.build_prefix/render_options/
        serialize_state) that agent.predict() itself uses below — never a
        second tokenizer, never a Go-side byte estimate. Tokenizer-only
        (CPU); no GPU forward pass, so this runs before every predict
        regardless of strict mode. build_prefix's own truncation decisions
        are reused unmodified; the two head/option text expressions below
        are duplicated only because build_prefix does not expose
        pre-truncation lengths, and must keep matching its own first lines
        exactly (asserted by this package's parity tests)."""
        if not isinstance(questions, dict):
            raise ValueError("questions must be a dictionary keyed by question id")
        max_len = agent.cfg.get("max_len", 512)
        head_max_len = agent.cfg.get("head_max_len", 192)
        mask = agent.tok.mask_token
        usage = {}
        for qid, qdef in questions.items():
            q = agent._to_internal(qdef)
            prefix_ids, prefix_markers = build_prefix(agent.tok, q, head_max_len)
            head_used = prefix_markers[0] - 2
            option_used = (len(prefix_ids) - 1) - prefix_markers[0]
            room = max(0, max_len - len(prefix_ids) - 1)

            head_text = "%s question: %s" % (q["t"], str(q["ins"]).replace(mask, " "))
            head_raw = len(agent.tok(head_text, add_special_tokens=False)["input_ids"])

            opts = render_options(q)
            option_raw = sum(
                1 + len(agent.tok(" " + opt.replace(mask, " "), add_special_tokens=False)["input_ids"])
                for opt in opts
            )

            state_raw = len(
                agent.tok(serialize_state(state).replace(mask, " "), add_special_tokens=False)["input_ids"]
            )
            state_used = min(state_raw, room)

            usage[qid] = dict(
                head_tokens=head_used,
                option_tokens=option_used,
                state_tokens=state_used,
                max_len=max_len,
                state_budget=room,
                head_truncated=head_raw > head_used,
                options_truncated=option_raw > option_used,
                state_truncated=state_raw > room,
            )
        return usage

    while True:
        try:
            request = receive()
        except EOFError:
            return
        if request.get("type") != "predict" or not isinstance(request.get("id"), int):
            send("error", code="protocol")
            return
        started = time.perf_counter()
        try:
            usage = measure_inputs(request["state"], request["questions"])
            lossy = any(
                u["head_truncated"] or u["options_truncated"] or u["state_truncated"]
                for u in usage.values()
            )
            if request.get("strict") and lossy:
                # Reject before the GPU forward pass — a strict caller must
                # never receive an answer computed from silently truncated
                # input.
                send("error", request["id"], code="capacity")
                continue
            result = agent.predict(request["state"], request["questions"])
            result["input_usage"] = usage
            send("result", request["id"], result=result,
                 predict_ms=(time.perf_counter() - started) * 1000)
        except (ValueError, TypeError, KeyError):
            # Do not reflect conversation text or upstream exception messages.
            send("error", request["id"], code="request")
        except Exception:
            send("error", request["id"], code="inference")
            return


try:
    main()
except EOFError:
    pass
except Exception:
    send("error", code="worker")
