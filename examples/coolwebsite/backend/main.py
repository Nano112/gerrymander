"""Toy API served at https://api.coolwebsite.test through gerrymander."""

import datetime

from fastapi import FastAPI
from fastapi.middleware.cors import CORSMiddleware

app = FastAPI(title="coolwebsite api")

# The frontend lives on a *different* hostname (coolwebsite.test), so the
# browser applies CORS. Allow exactly that origin — never "*" once cookies
# or auth headers enter the picture.
app.add_middleware(
    CORSMiddleware,
    allow_origins=["https://coolwebsite.test"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)


@app.get("/api/hello")
def hello() -> dict:
    return {
        "message": "hello from FastAPI behind gerrymander",
        "served_at": datetime.datetime.now(datetime.UTC).isoformat(),
    }


@app.get("/healthz")
def healthz() -> dict:
    return {"ok": True}
