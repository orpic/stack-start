import express from "express";

const app = express();
const port = Number(process.env.PORT ?? 3100);
const publicUrl = process.env.PUBLIC_URL ?? `http://127.0.0.1:${port}`;

app.get("/", (_req, res) => {
  res.json({ message: "Hello from node_api", publicUrl });
});

app.get("/health", (_req, res) => {
  res.json({ ok: true });
});

app.listen(port, "127.0.0.1", () => {
  console.log(`node_api listening on http://127.0.0.1:${port}`);
});
