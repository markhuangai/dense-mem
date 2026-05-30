package demo

import (
	"net/http"
	"os"
	"strings"

	"github.com/labstack/echo/v4"
)

func RegisterRoutes(e *echo.Echo, provisioner *Provisioner, configuredBaseURL string) {
	if strings.TrimSpace(configuredBaseURL) == "" {
		configuredBaseURL = os.Getenv("DEMO_PUBLIC_BASE_URL")
	}

	e.GET("/", func(c echo.Context) error {
		return c.HTML(http.StatusOK, landingHTML)
	})

	e.POST("/demo/api/session", func(c echo.Context) error {
		resp, err := provisioner.Provision(c.Request().Context(), ProvisionOptions{
			ClientIP: c.RealIP(),
			BaseURL:  requestBaseURL(c, configuredBaseURL),
		})
		if err != nil {
			return err
		}
		return c.JSON(http.StatusCreated, resp)
	})
}

func requestBaseURL(c echo.Context, configured string) string {
	if base := strings.TrimRight(strings.TrimSpace(configured), "/"); base != "" {
		return base
	}

	req := c.Request()
	proto := firstHeader(req.Header.Get("X-Forwarded-Proto"))
	if proto == "" {
		proto = c.Scheme()
	}
	if proto == "" {
		proto = "http"
	}

	host := firstHeader(req.Header.Get("X-Forwarded-Host"))
	if host == "" {
		host = req.Host
	}
	if host == "" {
		host = "localhost"
	}

	return proto + "://" + host
}

func firstHeader(value string) string {
	if idx := strings.Index(value, ","); idx >= 0 {
		value = value[:idx]
	}
	return strings.TrimSpace(value)
}

const landingHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>Dense-Mem Demo</title>
  <style>
    :root {
      color-scheme: light;
      --bg: #f7f8f5;
      --panel: #ffffff;
      --text: #17201b;
      --muted: #5f6b63;
      --line: #dfe5dd;
      --accent: #176b5b;
      --accent-2: #9b3c32;
      --code: #101614;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
      background: var(--bg);
      color: var(--text);
      line-height: 1.45;
    }
    main {
      width: min(1120px, calc(100vw - 32px));
      margin: 0 auto;
      padding: 40px 0 56px;
    }
    header {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 24px;
      align-items: end;
      padding-bottom: 24px;
      border-bottom: 1px solid var(--line);
    }
    h1 {
      margin: 0 0 10px;
      font-size: clamp(2rem, 6vw, 4.25rem);
      line-height: .95;
      letter-spacing: 0;
    }
    h2 {
      margin: 0 0 14px;
      font-size: 1.05rem;
      letter-spacing: 0;
    }
    p { margin: 0; color: var(--muted); max-width: 72ch; }
    .actions { display: flex; flex-wrap: wrap; gap: 10px; justify-content: flex-end; }
    button, a.button {
      min-height: 40px;
      border: 1px solid var(--accent);
      border-radius: 6px;
      padding: 0 14px;
      background: var(--accent);
      color: #fff;
      font: inherit;
      font-weight: 650;
      text-decoration: none;
      display: inline-flex;
      align-items: center;
      justify-content: center;
      cursor: pointer;
      white-space: nowrap;
    }
    button.secondary, a.button.secondary {
      background: transparent;
      color: var(--accent);
    }
    button:disabled { opacity: .6; cursor: wait; }
    .layout {
      display: grid;
      grid-template-columns: minmax(0, 1fr) 340px;
      gap: 22px;
      margin-top: 24px;
      align-items: start;
    }
    section {
      background: var(--panel);
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 18px;
    }
    .stack { display: grid; gap: 16px; }
    .key-row {
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 10px;
      align-items: center;
    }
    input {
      width: 100%;
      min-height: 44px;
      border: 1px solid var(--line);
      border-radius: 6px;
      padding: 0 12px;
      font: 600 .92rem ui-monospace, SFMono-Regular, Menlo, Consolas, monospace;
      color: var(--code);
      background: #fbfcfa;
    }
    .meta {
      display: grid;
      grid-template-columns: repeat(3, minmax(0, 1fr));
      gap: 10px;
      margin-top: 14px;
    }
    .metric {
      border: 1px solid var(--line);
      border-radius: 6px;
      padding: 10px;
      min-height: 74px;
      background: #fbfcfa;
    }
    .metric strong { display: block; font-size: 1.25rem; line-height: 1.15; }
    .metric span { color: var(--muted); font-size: .86rem; }
    pre {
      margin: 0;
      padding: 14px;
      overflow: auto;
      border-radius: 6px;
      background: var(--code);
      color: #edf7f1;
      font-size: .83rem;
      line-height: 1.55;
    }
    .snippet {
      display: grid;
      gap: 8px;
    }
    .snippet-head {
      display: flex;
      justify-content: space-between;
      gap: 10px;
      align-items: center;
    }
    .snippet-head button {
      min-height: 32px;
      padding: 0 10px;
      font-size: .86rem;
    }
    .notice {
      border-color: #e3b7b0;
      background: #fff7f5;
      color: var(--accent-2);
    }
    .notice p { color: var(--accent-2); }
    .status {
      min-height: 24px;
      color: var(--muted);
      font-size: .95rem;
    }
    @media (max-width: 860px) {
      main { width: min(100vw - 24px, 720px); padding-top: 24px; }
      header, .layout { grid-template-columns: 1fr; }
      .actions { justify-content: stretch; }
      button, a.button { width: 100%; }
      .meta { grid-template-columns: 1fr 1fr; }
      .key-row { grid-template-columns: 1fr; }
    }
    @media (max-width: 520px) {
      .meta { grid-template-columns: 1fr; }
    }
  </style>
</head>
<body>
  <main>
    <header>
      <div>
        <h1>Dense-Mem Demo</h1>
        <p>Generate a temporary isolated team, connect an MCP client, and try memory writes, recall, claims, verification, and fact promotion against the hosted demo instance.</p>
      </div>
      <div class="actions">
        <button id="newSession" class="secondary" type="button">Generate New Key</button>
        <a id="openUI" class="button" href="/ui">Open UI</a>
      </div>
    </header>

    <div class="layout">
      <div class="stack">
        <section>
          <h2>Demo Key</h2>
          <div class="key-row">
            <input id="apiKey" readonly value="Creating demo key..." aria-label="Demo API key">
            <button id="copyKey" type="button">Copy</button>
          </div>
          <div class="meta">
            <div class="metric"><strong id="expires">--</strong><span>expires</span></div>
            <div class="metric"><strong id="team">--</strong><span>team</span></div>
            <div class="metric"><strong id="profile">--</strong><span>profile</span></div>
          </div>
          <p class="status" id="status"></p>
        </section>

        <section>
          <h2>MCP Client Configuration</h2>
          <div class="stack">
            <div class="snippet">
              <div class="snippet-head">
                <strong>Streamable HTTP MCP</strong>
                <button type="button" data-copy-target="mcpJson">Copy</button>
              </div>
              <pre id="mcpJson"></pre>
            </div>
            <div class="snippet">
              <div class="snippet-head">
                <strong>mcp-remote proxy</strong>
                <button type="button" data-copy-target="mcpRemote">Copy</button>
              </div>
              <pre id="mcpRemote"></pre>
            </div>
            <div class="snippet">
              <div class="snippet-head">
                <strong>HTTP check</strong>
                <button type="button" data-copy-target="curlCheck">Copy</button>
              </div>
              <pre id="curlCheck"></pre>
            </div>
          </div>
        </section>
      </div>

      <aside class="stack">
        <section class="notice">
          <h2>Test Data Only</h2>
          <p>This hosted demo deletes expired teams and graph data. Store only disposable test information. Do not save secrets, personal data, credentials, production notes, or anything critical.</p>
        </section>
        <section>
          <h2>24 Hour Limits</h2>
          <div class="meta">
            <div class="metric"><strong id="qRequests">300</strong><span>requests</span></div>
            <div class="metric"><strong id="qWrites">75</strong><span>writes</span></div>
            <div class="metric"><strong id="qFragments">30</strong><span>save attempts</span></div>
            <div class="metric"><strong id="qBytes">128 KiB</strong><span>content</span></div>
            <div class="metric"><strong id="qClaims">30</strong><span>claims</span></div>
            <div class="metric"><strong id="qVerifier">10</strong><span>verifier</span></div>
            <div class="metric"><strong id="qFacts">5</strong><span>facts</span></div>
            <div class="metric"><strong id="qRecall">50</strong><span>recall</span></div>
            <div class="metric"><strong id="qRate">20/min</strong><span>rate</span></div>
          </div>
        </section>
      </aside>
    </div>
  </main>

  <script>
    const META_KEY = 'denseMem.demoSession';
    const USER_KEY = 'denseMem.userApiKey';

    const els = {
      apiKey: document.getElementById('apiKey'),
      copyKey: document.getElementById('copyKey'),
      newSession: document.getElementById('newSession'),
      openUI: document.getElementById('openUI'),
      expires: document.getElementById('expires'),
      team: document.getElementById('team'),
      profile: document.getElementById('profile'),
      status: document.getElementById('status'),
      mcpJson: document.getElementById('mcpJson'),
      mcpRemote: document.getElementById('mcpRemote'),
      curlCheck: document.getElementById('curlCheck')
    };

    function storedSession() {
      try {
        const raw = sessionStorage.getItem(META_KEY);
        if (!raw) return null;
        const session = JSON.parse(raw);
        if (!session || !session.api_key || !session.expires_at) return null;
        if (Date.parse(session.expires_at) <= Date.now() + 30000) return null;
        return session;
      } catch (_) {
        return null;
      }
    }

    function storeSession(session) {
      sessionStorage.setItem(META_KEY, JSON.stringify(session));
      sessionStorage.setItem(USER_KEY, session.api_key);
    }

    function setBusy(busy, message) {
      els.newSession.disabled = busy;
      els.copyKey.disabled = busy;
      els.status.textContent = message || '';
    }

    async function createSession() {
      const response = await fetch('/demo/api/session', { method: 'POST' });
      if (!response.ok) {
        let message = 'Could not create a demo key.';
        try {
          const body = await response.json();
          if (body && body.message) message = body.message;
        } catch (_) {}
        throw new Error(message);
      }
      return response.json();
    }

    async function ensureSession(force) {
      const existing = force ? null : storedSession();
      if (existing) {
        storeSession(existing);
        render(existing, 'Using the key already stored in this tab.');
        return;
      }

      setBusy(true, 'Creating an isolated 24 hour demo team...');
      try {
        const session = await createSession();
        storeSession(session);
        render(session, 'Demo key generated.');
      } catch (err) {
        els.apiKey.value = '';
        els.status.textContent = err.message;
      } finally {
        els.newSession.disabled = false;
        els.copyKey.disabled = false;
      }
    }

    function render(session, message) {
      const mcpURL = window.location.origin + '/mcp';
      const uiURL = window.location.origin + '/ui';
      const expires = new Date(session.expires_at);
      const quotas = session.quotas || {};

      els.apiKey.value = session.api_key;
      els.expires.textContent = expires.toLocaleString();
      els.team.textContent = shorten(session.team_id);
      els.profile.textContent = shorten(session.profile_id);
      els.openUI.href = uiURL;
      els.status.textContent = message || '';

      setText('qRequests', quotas.total_requests);
      setText('qWrites', quotas.write_attempts);
      setText('qFragments', quotas.fragment_attempts);
      setText('qBytes', formatBytes(quotas.fragment_bytes));
      setText('qClaims', quotas.created_claims);
      setText('qVerifier', quotas.verifier_attempts);
      setText('qFacts', quotas.promoted_facts);
      setText('qRecall', quotas.recall_calls);
      setText('qRate', (quotas.per_minute_requests || 20) + '/min');

      const config = {
        mcpServers: {
          'dense-mem-demo': {
            type: 'http',
            url: mcpURL,
            headers: {
              Authorization: 'Bearer ' + session.api_key
            }
          }
        }
      };
      els.mcpJson.textContent = JSON.stringify(config, null, 2);
      els.mcpRemote.textContent = 'npx -y mcp-remote ' + mcpURL + ' --header "Authorization: Bearer ' + session.api_key + '"';
      els.curlCheck.textContent = 'curl -s ' + quote(mcpURL) + ' \\\n  -H ' + quote('Authorization: Bearer ' + session.api_key) + ' \\\n  -H ' + quote('Content-Type: application/json') + ' \\\n  -d ' + quote('{"jsonrpc":"2.0","id":1,"method":"tools/list","params":{}}');
    }

    function setText(id, value) {
      if (value === undefined || value === null || value === '') return;
      document.getElementById(id).textContent = value;
    }

    function shorten(value) {
      if (!value || value.length < 12) return value || '--';
      return value.slice(0, 8) + '...' + value.slice(-4);
    }

    function formatBytes(value) {
      if (!value) return '128 KiB';
      return Math.round(value / 1024) + ' KiB';
    }

    function quote(value) {
      return "'" + String(value).replaceAll("'", "'\\''") + "'";
    }

    async function copyText(text) {
      await navigator.clipboard.writeText(text);
      els.status.textContent = 'Copied.';
    }

    els.copyKey.addEventListener('click', function () { copyText(els.apiKey.value); });
    els.newSession.addEventListener('click', function () { ensureSession(true); });
    document.querySelectorAll('[data-copy-target]').forEach(function (button) {
      button.addEventListener('click', function () {
        const target = document.getElementById(button.getAttribute('data-copy-target'));
        copyText(target.textContent);
      });
    });

    ensureSession(false);
  </script>
</body>
</html>`
