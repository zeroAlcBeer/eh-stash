import os
from pathlib import Path
from fastapi import FastAPI, HTTPException
from fastapi.middleware.cors import CORSMiddleware
from fastapi.responses import Response
from routers import admin, galleries, proxy, stats
from proxy_controller import start_worker

THUMB_DIR = Path(os.getenv("THUMB_DIR", "/data/thumbs"))

app = FastAPI(title="EH-Stash API")

app.add_middleware(
    CORSMiddleware,
    allow_origins=["*"],
    allow_credentials=True,
    allow_methods=["*"],
    allow_headers=["*"],
)

app.include_router(galleries.router)
app.include_router(stats.router)
app.include_router(admin.router)
app.include_router(proxy.router)


@app.on_event("startup")
def _startup():
    start_worker()


@app.get("/v1/thumbs/{gid}")
async def get_thumb(gid: int):
    path = THUMB_DIR / str(gid)
    if not path.exists():
        raise HTTPException(status_code=404, detail="Thumb not cached yet")
    return Response(
        content=path.read_bytes(),
        media_type="image/jpeg",
        headers={"Cache-Control": "public, max-age=604800"},  # 7 days
    )

@app.get("/")
def root():
    return {"message": "EH-Stash API is running"}


@app.get("/healthz")
def healthz():
    # Liveness probe for Uptime Kuma and the deploy Verify step. Kept cheap
    # (no DB round-trip) so a slow/blocked pool can't mask a live process.
    return {"status": "ok"}
