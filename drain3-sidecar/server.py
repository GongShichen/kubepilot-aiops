import asyncio
import json
import os
import re
import signal
import time
from collections import OrderedDict
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
from threading import Thread

from drain3 import TemplateMiner
from drain3.file_persistence import FilePersistence
from drain3.template_miner_config import TemplateMinerConfig
from prometheus_client import Counter, Histogram, start_http_server
from websockets.asyncio.server import serve

TOKEN = os.getenv("DRAIN3_TOKEN", "")
STATE_PATH = os.getenv("DRAIN3_STATE_PATH", "/data/drain3.bin")
MAX_BATCH = 500
MAX_CACHE = 5000
CACHE_TTL = 600
parsed = Counter("drain3_records_total", "Parsed records")
failed = Counter("drain3_errors_total", "Protocol errors")
latency = Histogram("drain3_batch_seconds", "Batch parse latency")

config = TemplateMinerConfig()
config.load(os.getenv("DRAIN3_CONFIG", "")) if os.getenv("DRAIN3_CONFIG") else None
miner = TemplateMiner(FilePersistence(STATE_PATH), config=config)
cache = OrderedDict()


def prune_cache(now):
    while cache:
        key, value = next(iter(cache.items()))
        if len(cache) <= MAX_CACHE and now - value[0] <= CACHE_TTL:
            break
        cache.pop(key, None)


def parameters(template, message):
    escaped = re.escape(template).replace(re.escape("<*>"), "(.*?)")
    match = re.fullmatch(escaped, message)
    return list(match.groups()) if match else []


def parse_batch(request):
    records = request.get("records")
    if not isinstance(records, list) or len(records) > MAX_BATCH:
        raise ValueError(f"records must be a list with at most {MAX_BATCH} items")
    results = []
    for record in records:
        record_id = record.get("record_id")
        message = record.get("message")
        if not record_id or not isinstance(message, str):
            raise ValueError("record_id and string message are required")
        result = miner.add_log_message(message)
        template = result["template_mined"]
        results.append({
            "record_id": record_id,
            "cluster_id": result["cluster_id"],
            "template": template,
            "parameters": parameters(template, message),
            "occurrence_count": result["cluster_count"],
        })
        parsed.inc()
    return results


async def handler(websocket):
    auth = websocket.request.headers.get("Authorization", "")
    if not TOKEN or auth != f"Bearer {TOKEN}":
        await websocket.close(code=4401, reason="unauthorized")
        return
    async for raw in websocket:
        request_id = ""
        try:
            if len(raw) > 2 * 1024 * 1024:
                raise ValueError("message exceeds 2MB")
            request = json.loads(raw)
            request_id = request.get("request_id", "")
            if request.get("version") != "1" or request.get("type") != "parse_batch" or not request_id:
                raise ValueError("version=1, type=parse_batch and request_id are required")
            now = time.time()
            prune_cache(now)
            if request_id in cache:
                response = cache[request_id][1]
            else:
                started = time.monotonic()
                results = parse_batch(request)
                latency.observe(time.monotonic() - started)
                response = {"version": "1", "type": "parse_result", "request_id": request_id, "results": results}
                cache[request_id] = (now, response)
            await websocket.send(json.dumps(response, separators=(",", ":")))
        except Exception as exc:
            failed.inc()
            response = {"version": "1", "type": "error", "request_id": request_id, "code": "INVALID_RECORD", "message": str(exc)[:300]}
            await websocket.send(json.dumps(response, separators=(",", ":")))


class HealthHandler(BaseHTTPRequestHandler):
    def do_GET(self):
        if self.path not in ("/healthz", "/readyz"):
            self.send_response(404)
            self.end_headers()
            return
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.end_headers()
        self.wfile.write(b'{"status":"ok"}')

    def log_message(self, _format, *_args):
        return


async def main():
    Thread(target=lambda: ThreadingHTTPServer(("0.0.0.0", 8082), HealthHandler).serve_forever(), daemon=True).start()
    start_http_server(9091)
    stop = asyncio.get_running_loop().create_future()
    for sig in (signal.SIGINT, signal.SIGTERM):
        asyncio.get_running_loop().add_signal_handler(sig, lambda: not stop.done() and stop.set_result(None))
    async with serve(handler, "0.0.0.0", 8081, max_size=2 * 1024 * 1024, ping_interval=20, ping_timeout=60):
        await stop
    miner.save_state("shutdown")


if __name__ == "__main__":
    asyncio.run(main())
