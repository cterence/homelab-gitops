# Build stage: Kaniko clones the repo into a scratch context and runs this
# Dockerfile, so the source tree is available. Dependencies are compiled and
# installed into site-packages, then copied into the runtime image.
FROM python:3.14-alpine@sha256:016508ba505da24f7139765bc4bb669df4e88eb2f12eeadd571bf2f88d7533df AS builder

WORKDIR /app

COPY pyproject.toml .

RUN pip install uv && \
    uv pip compile pyproject.toml -o requirements.txt && \
    uv pip install --system -r requirements.txt

FROM python:3.14-alpine@sha256:016508ba505da24f7139765bc4bb669df4e88eb2f12eeadd571bf2f88d7533df

WORKDIR /app

COPY --from=builder /usr/local/lib/python3.14/site-packages /usr/local/lib/python3.14/site-packages
COPY main.py .

ENTRYPOINT ["python", "main.py"]

LABEL org.opencontainers.container.app="mcp-telegram"
