# ── builder ────────────────────────────────────────────────
FROM python:3.11-slim AS builder

COPY --from=ghcr.io/astral-sh/uv:latest /uv /bin/

RUN apt-get update && apt-get install -y --no-install-recommends \
    libpq-dev gcc python3-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY requirements.txt .

RUN uv venv --no-seed /opt/venv && \
    uv pip install --python /opt/venv --no-cache -r requirements.txt

# ── runtime ────────────────────────────────────────────────
FROM python:3.11-slim

# psycopg2-binary 自带 libpq.so，无需任何 apt 依赖
COPY --from=builder /opt/venv /opt/venv
ENV PATH="/opt/venv/bin:$PATH"

WORKDIR /app
COPY . .

CMD ["/opt/venv/bin/uvicorn", "main:app", "--host", "0.0.0.0", "--port", "3000"]
