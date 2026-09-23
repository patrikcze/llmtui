"""Generate GoldenFixture files from direct, pinned laya-mlx (not the worker).

python reference.py --store <llmtui models/laya> --output <fixture directory>
Only local models are used. A socket guard makes attempted network I/O fail.
"""
import argparse
import importlib.metadata
import json
import os
import platform
import socket
from pathlib import Path

os.environ["HF_HUB_OFFLINE"] = "1"
os.environ["HF_HUB_DISABLE_TELEMETRY"] = "1"


def offline(*args, **kwargs):
    raise RuntimeError("network access forbidden during reference inference")


socket.socket.connect = offline
socket.create_connection = offline
import laya_mlx as laya

PINNED = {
    "english-mlx": "047678560251f28113ee8f5df4be82102c7bf336",
    "multilingual-mlx": "ba40c87fcb357f1643d04d71323af9cdc3b9e591",
    "typed-decisions-mlx": "28416e78cb26a239a4eabaa2e084904ec5e6cacb",
}
parser = argparse.ArgumentParser(description=__doc__)
parser.add_argument("--store", type=Path, required=True)
parser.add_argument("--output", type=Path, required=True)
args = parser.parse_args()
version = importlib.metadata.version("laya-mlx")
assert version == "0.2.0", version
# Go encoding/json sorts map keys; use the identical option and state order.
request = json.loads(json.dumps(json.loads(Path(__file__).with_name("request.json").read_text()), sort_keys=True))
args.output.mkdir(parents=True, exist_ok=True)
for alias, revision in PINNED.items():
    path = (args.store / alias / revision).resolve(strict=True)
    agent = laya.load(path, dtype="float16", device="gpu", batch_size=16, compile=False, cache_prompts=False)
    cases = [("duplicate_charge", request["state"])]
    if alias == "multilingual-mlx":
        cases.append(("french_refund", "Ma facture a été débitée deux fois. Remboursez-moi aujourd'hui, sinon je résilie mon abonnement."))
    golden = []
    for name, state in cases:
        answers = agent.predict(state, request["questions"])["answers"]
        normalized = {}
        for key, answer in answers.items():
            fields = {k: v for k, v in answer.items() if k in ("type", "choice", "score", "confidence", "probabilities")}
            if answer["type"] == "noul":
                fields["probability"] = answer["noul"]
            normalized[key] = fields
        golden.append(dict(name=name, state=state, questions=request["questions"], expected=dict(answers=normalized)))
    fixture = dict(schema_version=1, upstream_revision=revision, cases=golden)
    (args.output / (alias + ".json")).write_text(json.dumps(fixture, indent=2, ensure_ascii=False) + "\n")
    provenance = dict(laya_mlx_version=version, model_revision=revision, dtype="float16", device="gpu", platform=platform.platform(), probability_tolerance=0.002, network="socket connections forbidden", reference="direct laya_mlx.Agent.predict", compile=False, cache_prompts=False)
    (args.output / (alias + ".provenance.json")).write_text(json.dumps(provenance, indent=2) + "\n")
    print(alias, normalized["department"]["choice"], flush=True)
    del agent
