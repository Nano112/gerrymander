import { useEffect, useState } from "react";

// Different hostname, same trust domain: the browser applies CORS, the
// backend allows exactly https://coolwebsite.test.
const API = "https://backend.coolwebsite.test";

export default function App() {
  const [reply, setReply] = useState<string>("calling the api…");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    fetch(`${API}/api/hello`)
      .then((r) => r.json())
      .then((d) => setReply(`${d.message} @ ${d.served_at}`))
      .catch((e) => setError(String(e)));
  }, []);

  return (
    <main style={{ fontFamily: "system-ui", maxWidth: 640, margin: "4rem auto", lineHeight: 1.6 }}>
      <h1>coolwebsite.test</h1>
      <p>
        Vite + React on <code>https://coolwebsite.test</code>, FastAPI on{" "}
        <code>https://api.coolwebsite.test</code> — hostnames, sticky ports,
        TLS, and routing all from one <code>gerrymander.yaml</code>.
      </p>
      <p data-testid="api-reply">
        <strong>API says:</strong> {error ?? reply}
      </p>
      <p style={{ opacity: 0.6 }}>Edit <code>src/App.tsx</code> — HMR flows over wss://coolwebsite.test.</p>
    </main>
  );
}
