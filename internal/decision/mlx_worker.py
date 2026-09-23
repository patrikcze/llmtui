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
PROTOCOL = 1
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
            result = agent.predict(request["state"], request["questions"])
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
