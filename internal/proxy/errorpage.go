package proxy

import (
	"html/template"
	"net/http"
	"strings"
	"time"
)

// Browser-facing diagnostic pages. Plain text stays the contract for
// non-browser clients (curl, health checks, scripts) — only requests that
// explicitly Accept text/html get the page. Every error response carries
// X-Gerry-Error so the page's recovery poller can tell "still broken" from
// "backend is back" without parsing bodies.
const errHeader = "X-Gerry-Error"

// wantsHTML reports whether the client asked for a web page.
func wantsHTML(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "text/html")
}

type errPageData struct {
	Code     int
	Title    string
	Headline string
	Host     string
	Detail   string          // one-sentence diagnosis
	Upstream string          // target that failed, when known
	Hints    []template.HTML // concrete next moves, pre-escaped
	Retry    bool            // poll for recovery and reload
	Time     string
}

// hint escapes a plain sentence; when it ends in ": command", the command
// part is wrapped in <code> so it's copyable.
func hint(s string) template.HTML {
	esc := template.HTMLEscapeString
	if i := strings.Index(s, ": "); i > -1 {
		return template.HTML(esc(s[:i]) + ": <code>" + esc(s[i+2:]) + "</code>")
	}
	return template.HTML(esc(s))
}

var errPageTpl = template.Must(template.New("err").Parse(`<!doctype html>
<html lang="en">
<head>
<meta charset="utf-8">
<meta name="viewport" content="width=device-width, initial-scale=1">
<title>{{.Code}} · {{.Host}}</title>
<style>
  :root{
    --desk:#17130f;--desk-raise:#201a14;--desk-line:#322a20;
    --ink:#ece4d4;--ink-dim:#9d9280;--ink-faint:#6b6254;
    --crayon:#8f6fe0;--green:#7fa653;--vermilion:#c75b4a;--parchment:#e8dcbb;
  }
  *{box-sizing:border-box;margin:0;padding:0}
  body{
    background:var(--desk);color:var(--ink);
    font:14px/1.6 -apple-system,"SF Pro Text","Segoe UI",system-ui,sans-serif;
    min-height:100vh;display:grid;place-items:center;padding:24px;
  }
  main{max-width:560px;width:100%}
  .plate{
    border:1px dashed #4a3f30;border-radius:10px;background:var(--desk-raise);
    padding:28px 30px;
  }
  .eyebrow{
    font-size:11px;letter-spacing:.14em;text-transform:uppercase;
    color:var(--ink-faint);margin-bottom:14px;display:flex;gap:8px;align-items:center;
  }
  .blob{width:9px;height:9px;border-radius:50% 45% 55% 48%;transform:rotate(8deg);background:var(--vermilion);flex:none}
  .blob.ok{background:var(--green)}
  h1{
    font-family:"Iowan Old Style",Georgia,serif;font-size:22px;font-weight:600;
    letter-spacing:.01em;margin-bottom:10px;
  }
  h1 .host{color:var(--crayon)}
  p.detail{color:var(--ink-dim);margin-bottom:18px}
  .upstream{
    font:12.5px ui-monospace,"SF Mono",Menlo,monospace;color:var(--parchment);
    background:var(--desk);border:1px solid var(--desk-line);border-radius:6px;
    padding:9px 12px;margin-bottom:18px;overflow-x:auto;white-space:nowrap;
  }
  ul{list-style:none}
  li{
    color:var(--ink-dim);padding:5px 0 5px 18px;position:relative;
  }
  li::before{content:"—";position:absolute;left:0;color:var(--ink-faint)}
  li code{
    font:12px ui-monospace,"SF Mono",Menlo,monospace;color:var(--ink);
    background:var(--desk);border:1px solid var(--desk-line);border-radius:4px;padding:1px 6px;
    user-select:all;
  }
  footer{
    margin-top:16px;display:flex;justify-content:space-between;align-items:baseline;
    font-size:11px;color:var(--ink-faint);
  }
  footer .mark{font-family:"Iowan Old Style",Georgia,serif;font-style:italic}
  #pulse{color:var(--ink-faint)}
  #pulse.live{color:var(--green)}
  @media (prefers-reduced-motion:no-preference){
    .blob{animation:squish 2.4s ease-in-out infinite}
    @keyframes squish{50%{transform:rotate(8deg) scale(.82)}}
  }
</style>
</head>
<body>
<main>
  <div class="plate">
    <div class="eyebrow"><span class="blob" id="blob"></span>{{.Code}} · {{.Title}}</div>
    <h1><span class="host">{{.Host}}</span> {{.Headline}}</h1>
    <p class="detail">{{.Detail}}</p>
    {{if .Upstream}}<div class="upstream">{{.Upstream}}</div>{{end}}
    {{if .Hints}}<ul>{{range .Hints}}<li>{{.}}</li>{{end}}</ul>{{end}}
    <footer>
      <span class="mark">gerrymander · district office</span>
      <span id="pulse">{{if .Retry}}watching for the backend…{{else}}{{.Time}}{{end}}</span>
    </footer>
  </div>
</main>
{{if .Retry}}
<script>
  // Poll this same URL; the daemon marks its own failures with X-Gerry-Error.
  // The moment a response arrives without it, the backend is back — reload.
  const pulse = document.getElementById("pulse");
  const blob = document.getElementById("blob");
  let tries = 0;
  async function probe(){
    tries++;
    try {
      const r = await fetch(location.href, {method:"HEAD", cache:"no-store"});
      if (!r.headers.get("x-gerry-error")) {
        pulse.textContent = "back — reloading";
        pulse.classList.add("live"); blob.classList.add("ok");
        setTimeout(() => location.reload(), 300);
        return;
      }
    } catch {}
    pulse.textContent = "watching for the backend… (" + tries + ")";
    setTimeout(probe, tries < 15 ? 2000 : 5000);
  }
  setTimeout(probe, 2000);
</script>
{{end}}
</body>
</html>
`))

// Hint builders keep the advice concrete instead of generic.
func hintsForBackend(t Target) []template.HTML {
	var hints []template.HTML
	b := t.Backend
	switch {
	case b.Kind == "address" && b.Address != nil:
		hints = append(hints,
			hint("The route is fine — the process behind it isn’t listening yet, or just stopped."),
			template.HTML("Owned by <code>"+template.HTMLEscapeString(orDash(t.Alloc.OwnerRef))+"</code> — start that project’s dev server and this page reloads itself."),
		)
		if t.Alloc.OwnerRef != "" {
			hints = append(hints, hint("Sticky ports survive restarts: gerry run --owner "+t.Alloc.OwnerRef+" -- …"))
		}
	case b.Kind == "supervised":
		hints = append(hints,
			hint("The supervised process failed to become healthy — check its logs: gerry ls"))
	default:
		hints = append(hints, hint("Check the backend service this route points at."))
	}
	return hints
}

func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// writeUnknownHost renders the 404 for hosts with no allocation.
func writeUnknownHost(w http.ResponseWriter, r *http.Request, host string, listenPort int, zones []string) {
	w.Header().Set(errHeader, "no-route")
	if !wantsHTML(r) {
		http.Error(w, "gerrymander: no allocation routes \""+host+"\" on port "+itoa(listenPort)+"\n", http.StatusNotFound)
		return
	}
	hints := []template.HTML{}
	for _, z := range zones {
		if strings.HasSuffix(host, "."+z) || host == z {
			label := strings.TrimSuffix(strings.TrimSuffix(host, z), ".")
			if label == "" {
				label = "@"
			}
			hints = append(hints, hint("Claim it: gerry claim --zone "+z+" --label "+label))
			break
		}
	}
	hints = append(hints,
		hint("See what routes where: gerry ls"),
		hint("Or declare it in a gerrymander.yaml and run: gerry up"))
	renderErrPage(w, r, errPageData{
		Code: 404, Title: "unclaimed district", Headline: "isn’t claimed yet", Host: host,
		Detail: "No allocation in the registry routes this hostname" + portSuffix(listenPort) + ".",
		Hints:  hints,
	})
}

// writeUpstreamDown renders the 502 for known hosts whose backend failed.
func writeUpstreamDown(w http.ResponseWriter, r *http.Request, t Target, upstream string, cause error) {
	w.Header().Set(errHeader, "upstream")
	if !wantsHTML(r) {
		http.Error(w, "gerrymander: \""+t.Alloc.FQDN+"\" upstream "+upstream+" failed: "+cause.Error()+"\n", http.StatusBadGateway)
		return
	}
	renderErrPage(w, r, errPageData{
		Code: 502, Title: "backend unreachable", Headline: "isn’t answering", Host: t.Alloc.FQDN,
		Detail:   "The route exists and TLS is fine — the upstream refused or dropped the connection.",
		Upstream: upstream + "   ·   " + cause.Error(),
		Hints:    hintsForBackend(t), Retry: true,
	})
}

func renderErrPage(w http.ResponseWriter, r *http.Request, d errPageData) {
	d.Time = time.Now().Format("15:04:05")
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(d.Code)
	if r.Method == http.MethodHead {
		return
	}
	errPageTpl.Execute(w, d)
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [12]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}

func portSuffix(p int) string {
	if p == 443 || p == 80 || p == 0 {
		return ""
	}
	return " on port " + itoa(p)
}
