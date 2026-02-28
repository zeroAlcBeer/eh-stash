# ── builder ────────────────────────────────────────────────
FROM python:3.11-slim AS builder

COPY --from=ghcr.io/astral-sh/uv:latest /uv /bin/

RUN apt-get update && apt-get install -y --no-install-recommends \
    libpq-dev gcc python3-dev libxml2-dev libxslt-dev \
    && rm -rf /var/lib/apt/lists/*

WORKDIR /app
COPY requirements.txt .

RUN uv venv --no-seed /opt/venv && \
    uv pip install --python /opt/venv/bin/python --link-mode=copy -r requirements.txt && \
    /opt/venv/bin/python -c "import curl_cffi"

# ── runtime ────────────────────────────────────────────────
FROM python:3.11-slim

# lxml 运行时需要 libxml2 / libxslt .so；psycopg2-binary / curl-cffi 均自带依赖
RUN apt-get update && apt-get install -y --no-install-recommends \
    libxml2 libxslt1.1 \
    && rm -rf /var/lib/apt/lists/*

COPY --from=builder /opt/venv /opt/venv
ENV PATH="/opt/venv/bin:$PATH"

WORKDIR /app
COPY . .

CMD ["/opt/venv/bin/python", "main.py"]
