"""Serves the extension's real pages with a fake browser, for working on the UI
without Firefox or the engine.

    python extension/dev/serve.py 8765
    http://127.0.0.1:8765/ui/popup.html?fixture=downloads
    http://127.0.0.1:8765/ui/settings.html?fixture=downloads

The pages under ui/ get dev/fake-browser.js and dev/fixtures.js injected
before their own scripts; ?fixture= picks the state they are fed (see
fixtures.js). Everything else is served as it is in the extension folder.
"""
import http.server
import os
import sys

EXTENSION = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
INJECT = b'<script src="/dev/fake-browser.js"></script><script src="/dev/fixtures.js"></script>\n'


class Handler(http.server.SimpleHTTPRequestHandler):
    def __init__(self, *args, **kwargs):
        super().__init__(*args, directory=EXTENSION, **kwargs)

    def do_GET(self):
        path = self.path.split("?", 1)[0]
        if path.startswith("/ui/") and path.endswith(".html"):
            file = os.path.join(EXTENSION, path.lstrip("/"))
            try:
                with open(file, "rb") as f:
                    page = f.read()
            except OSError:
                return self.send_error(404)
            i = page.find(b"<script")
            page = page[:i] + INJECT + page[i:] if i >= 0 else page + INJECT
            self.send_response(200)
            self.send_header("Content-Type", "text/html; charset=utf-8")
            self.send_header("Content-Length", str(len(page)))
            self.send_header("Cache-Control", "no-store")
            self.end_headers()
            self.wfile.write(page)
            return
        super().do_GET()

    def end_headers(self):
        self.send_header("Cache-Control", "no-store")
        super().end_headers()


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8765
    http.server.ThreadingHTTPServer(("127.0.0.1", port), Handler).serve_forever()
