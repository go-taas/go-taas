#!/bin/sh
# Ad-hoc mock inference server for compose verification (features #44, #42
# and #48). Serves the OpenAI-compatible POST /v1/chat/completions with a
# deterministic completion and token usage, plus GET /v1/models for the
# external-provider connectivity probe (feature #48).
#
# Feature #48 prefix variants (test infrastructure only): a base path of
# /unauthorized or /unavailable makes the model-list and chat endpoints
# return 401 or 503 so the compose stack can exercise all five probe states
# and both upstream failure mappings without external network:
#   GET  /v1/models                 -> 200  (probe ok)
#   GET  /unauthorized/models       -> 401  (probe unauthorized)
#   GET  /unavailable/models        -> 503  (probe error)
#   POST /v1/chat/completions       -> 200  (proxied external call)
#   POST /unauthorized/chat/completions -> 401 (upstream -> 13012)
#   POST /unavailable/chat/completions  -> 503 (upstream -> 13013)
#
# Usage: docker run --rm --name mock-infer --network go-taas_default \
#         -p 18000:8000 -v <repo>/test/e2e/mock/mock_infer.py:/srv/app.py \
#         python:3.12-alpine sh /srv/app.py
from http.server import BaseHTTPRequestHandler, HTTPServer
import json


def classify(path):
    """Split an optional /unauthorized or /unavailable prefix and an
    optional /v1 prefix from the request path."""
    variant = "ok"
    for prefix in ("/unauthorized", "/unavailable"):
        if path.startswith(prefix):
            variant = prefix.strip("/")
            path = path[len(prefix):]
            break
    if path.startswith("/v1"):
        path = path[len("/v1"):]
    return variant, path


class Handler(BaseHTTPRequestHandler):
    def _json(self, status, payload):
        data = json.dumps(payload).encode()
        self.send_response(status)
        self.send_header("Content-Type", "application/json")
        self.send_header("Content-Length", str(len(data)))
        self.end_headers()
        self.wfile.write(data)

    def do_GET(self):
        variant, path = classify(self.path)
        # The health endpoint stays variant-free.
        if path == "/healthz":
            self.send_response(200)
            self.end_headers()
            self.wfile.write(b"ok")
            return
        if path == "/models":
            if variant == "unauthorized":
                self._json(401, {"error": {"message": "invalid api key"}})
                return
            if variant == "unavailable":
                self._json(503, {"error": {"message": "service unavailable"}})
                return
            self._json(200, {"object": "list", "data": [{"id": "gpt-4o", "object": "model"}]})
            return
        self.send_response(404)
        self.end_headers()

    def do_POST(self):
        variant, path = classify(self.path)
        if path != "/chat/completions":
            self.send_response(404)
            self.end_headers()
            return
        length = int(self.headers.get("Content-Length", 0))
        body = self.rfile.read(length)
        if variant == "unauthorized":
            self._json(401, {"error": {"message": "invalid api key"}})
            return
        if variant == "unavailable":
            self._json(503, {"error": {"message": "service unavailable"}})
            return
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
        self._json(200, completion)

    def log_message(self, fmt, *args):
        print("mock-infer:", fmt % args, flush=True)


if __name__ == "__main__":
    HTTPServer(("0.0.0.0", 8000), Handler).serve_forever()
