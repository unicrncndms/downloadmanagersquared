"""A landing page to test resolving against, standing in for a public file host
that gives each IP its own download link.

    python extension/dev/landing.py 8780
    http://127.0.0.1:8780/

Two clicks lead to the file: "Free download" on the start page, then
"Download" on the wait page. Per URL (query string, carried along):

    /instant/start                 both buttons ready at once
    /timed/start?wait=10           a countdown on the wait page, paused while
                                   the page is hidden, then the button is made
                                   ready by
      &ready=disabled              removing `disabled` (default)
      &ready=pointer               removing `pointer-events: none`
      &ready=href                  filling the link's empty href
      &overlay=1                   a layer covers the button until the countdown ends
      &popup=1                     the first click also opens a pop-up-ad tab on another site
      &size=200                    file size in MB (default 200)

The start page sets a session cookie. The download URL's token is bound to the
client's IP and that cookie, so it works only through the interface, and in
the container, that opened the page. The file is served with range support.
It listens on every local address, so a token can be fetched over two
interfaces of the same machine.
"""
import html
import http.server
import re
import secrets
import sys
import urllib.parse

TOKENS = {}  # token -> (client ip, session cookie, size)
COOKIE = "dms_fixture"


def page(title, body):
    return f"""<!doctype html><html><head><meta charset="utf-8"><title>{html.escape(title)}</title>
<style>
  body {{ font: 15px system-ui, sans-serif; max-width: 640px; margin: 40px auto; }}
  .box {{ position: relative; display: inline-block; margin-top: 16px; }}
  .btn {{ display: inline-block; padding: 10px 22px; border-radius: 6px; background: #2563eb; color: #fff;
          text-decoration: none; border: 0; font: inherit; cursor: pointer; }}
  .btn[disabled], .btn.wait {{ background: #9ca3af; }}
  #cover {{ position: absolute; inset: -6px; background: rgba(255,255,255,.85); display: flex;
            align-items: center; justify-content: center; font-size: 13px; }}
</style></head><body>{body}</body></html>"""


class Handler(http.server.BaseHTTPRequestHandler):
    def do_GET(self):
        url = urllib.parse.urlsplit(self.path)
        q = dict(urllib.parse.parse_qsl(url.query))
        qs = ("?" + url.query) if url.query else ""
        m = re.fullmatch(r"/(instant|timed)/(start|wait|get)", url.path)
        if url.path == "/":
            return self.index()
        if url.path == "/ad":
            return self.send_page(page("Ad", "<h1>An ad</h1><p>A pop-up DMS² should close.</p>"))
        if m:
            return getattr(self, m.group(2))(m.group(1), q, qs)
        m = re.fullmatch(r"/file/([0-9a-f]+)/[^/]+", url.path)
        if m:
            return self.file(m.group(1))
        self.send_error(404)

    do_HEAD = do_GET

    def index(self):
        links = [
            ("/instant/start", "instant"),
            ("/timed/start?wait=10", "timed, button disabled"),
            ("/timed/start?wait=10&ready=pointer&overlay=1", "timed, pointer-events and an overlay"),
            ("/timed/start?wait=10&ready=href&popup=1", "timed, empty href and a pop-up ad"),
        ]
        items = "".join(f'<li><a href="{u}">{t}</a></li>' for u, t in links)
        self.send_page(page("Fixture file host", f"<h1>Fixture file host</h1><ul>{items}</ul>"))

    def start(self, variant, q, qs):
        popup = ""
        if q.get("popup") == "1":
            host = self.headers.get("Host", "")
            other = host.replace("127.0.0.1", "localhost") if "127.0.0.1" in host else host.replace("localhost", "127.0.0.1")
            popup = f"""<script>
document.getElementById("go").addEventListener("click", function once() {{
  this.removeEventListener("click", once);
  window.open("http://{other}/ad", "_blank");
}});
</script>"""
        body = f"""<h1>fixture.bin</h1><p>{q.get('size', '200')} MB</p>
<a id="go" class="btn" href="/{variant}/wait{qs}">Free download</a>{popup}"""
        cookie = self.cookie() or secrets.token_hex(8)
        self.send_page(page("Start", body), {"Set-Cookie": f"{COOKIE}={cookie}; Path=/; SameSite=Lax"})

    def wait(self, variant, q, qs):
        wait = int(q.get("wait", "0")) if variant == "timed" else 0
        ready = q.get("ready", "disabled")
        get = f"/{variant}/get{qs}"
        if wait == 0:
            btn = f'<a id="dl" class="btn" href="{get}">Download</a>'
        elif ready == "href":
            btn = f'<a id="dl" class="btn wait" href="">Download</a>'
        elif ready == "pointer":
            btn = f'<a id="dl" class="btn wait" href="{get}" style="pointer-events: none">Download</a>'
        else:
            btn = f'<button id="dl" class="btn" disabled onclick="location.href=\'{get}\'">Download</button>'
        cover = '<div id="cover">Please wait…</div>' if q.get("overlay") == "1" and wait else ""
        script = ""
        if wait:
            script = f"""<script>
let left = {wait};
const dl = document.getElementById("dl"), t = document.getElementById("t");
const tick = setInterval(() => {{
  if (document.hidden) return; // like many hosts: the countdown runs only while you look
  t.textContent = --left;
  if (left > 0) return;
  clearInterval(tick);
  dl.classList.remove("wait");
  if ({ready!r} === "href") dl.setAttribute("href", {get!r});
  else if ({ready!r} === "pointer") dl.style.pointerEvents = "";
  else dl.disabled = false;
  const c = document.getElementById("cover");
  if (c) c.remove();
}}, 1000);
</script>"""
        countdown = f"<p>Your download is ready in <b id=\"t\">{wait}</b> s.</p>" if wait else "<p>Your download is ready.</p>"
        self.send_page(page("Wait", f"<h1>fixture.bin</h1>{countdown}<div class=\"box\">{btn}{cover}</div>{script}"))

    def get(self, variant, q, qs):
        session = self.cookie()
        if not session:
            return self.send_error(403, "no session: open the start page first")
        token = secrets.token_hex(12)
        TOKENS[token] = (self.client_address[0], session, int(q.get("size", "200")) << 20)
        self.send_response(302)
        self.send_header("Location", f"/file/{token}/fixture.bin")
        self.send_header("Content-Length", "0")
        self.end_headers()

    def file(self, token):
        bound = TOKENS.get(token)
        if not bound or bound[0] != self.client_address[0] or bound[1] != self.cookie():
            return self.send_error(403, "this link belongs to another IP or session")
        size = bound[2]
        start, end, status = 0, size - 1, 200
        m = re.fullmatch(r"bytes=(\d*)-(\d*)", self.headers.get("Range", "").strip())
        if m and (m.group(1) or m.group(2)):
            if m.group(1):
                start = int(m.group(1))
                end = min(int(m.group(2)), size - 1) if m.group(2) else size - 1
            else:
                start = max(size - int(m.group(2)), 0)
            if start > end:
                self.send_response(416)
                self.send_header("Content-Range", f"bytes */{size}")
                self.end_headers()
                return
            status = 206
        self.send_response(status)
        self.send_header("Content-Type", "application/octet-stream")
        self.send_header("Content-Disposition", 'attachment; filename="fixture.bin"')
        self.send_header("Accept-Ranges", "bytes")
        self.send_header("Content-Length", str(end - start + 1))
        if status == 206:
            self.send_header("Content-Range", f"bytes {start}-{end}/{size}")
        self.end_headers()
        if self.command == "HEAD":
            return
        # Byte i of the file is i mod 251, so any range can be checked.
        block = bytes(i % 251 for i in range(251 * 1024))
        pos = start
        try:
            while pos <= end:
                off = pos % 251
                n = min(len(block) - off, end - pos + 1)
                self.wfile.write(block[off:off + n])
                pos += n
        except (BrokenPipeError, ConnectionResetError):
            pass

    def cookie(self):
        for part in self.headers.get("Cookie", "").split(";"):
            k, _, v = part.strip().partition("=")
            if k == COOKIE:
                return v
        return ""

    def send_page(self, body, headers=None):
        b = body.encode()
        self.send_response(200)
        self.send_header("Content-Type", "text/html; charset=utf-8")
        self.send_header("Content-Length", str(len(b)))
        self.send_header("Cache-Control", "no-store")
        for k, v in (headers or {}).items():
            self.send_header(k, v)
        self.end_headers()
        if self.command != "HEAD":
            self.wfile.write(b)


if __name__ == "__main__":
    port = int(sys.argv[1]) if len(sys.argv) > 1 else 8780
    http.server.ThreadingHTTPServer(("0.0.0.0", port), Handler).serve_forever()
