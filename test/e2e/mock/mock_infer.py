#!/bin/sh
# Ad-hoc mock inference server for compose verification of the
# evaluation feature (feature #44). Serves POST /v1/chat/completions
# with a deterministic completion and token usage, mirroring the
# OpenAI-compatible schema the infer service proxies.
#
# Usage: docker run --rm --name mock-infer --network go-taas_default \
#         -p 18000:8000 -v <repo>/test/e2e/mock/mock_infer.py:/srv/app.py \
#         python:3.12-alpine sh /srv/app.py
from http.server import BaseHTTPRequestHandler, HTTPServer
import json


class Handler(BaseHTTPRequestHandler):
    def do_POST(self):
        if self.path != "/v1/chat/completions":
            self.send_response(404)
            self.end_headers()
            return
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length)
        try:
            payload = json.loads(body)
        except Exception:
            payload = {}
        # Deterministic answer that satisfies the "contains 42" check.
        content = "The answer is 42."
        prompt_text = json.dumps(payload.get("messages", []))
        completion = {
            "choices": [{"message": {"role": "assistant", "content": content}}],
            "usage": {
                "prompt_tokens": max(1, len(prompt_text) // 4),
                "completion_tokens": 6,
            },
        }
        data = json.dumps(completion).encode()
        self.send_response(200)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        if self.path == "/healthz":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
            return
        self.send_response(404)
        self.end_headers()

    def log_message(self, fmt, *args):
        print("mock-infer:", fmt % args, flush=True)


if __name__ == "__main__":
    HTTPServer(("0.0.0.0", 8000), Handler).serve_forever()
