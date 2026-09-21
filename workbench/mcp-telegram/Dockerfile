# Build stage: Kaniko clones the repo into a scratch context and runs this
# Dockerfile, so the source tree is available. Dependencies are compiled and
# installed into site-packages, then copied into the runtime image.
FROM python:3.14-alpine@sha256:9e9fde4d32eedce0b661d9ab91e826b62dddf28e928c230ec55f1866cac66b01 AS builder

WORKDIR /app

COPY pyproject.toml .

RUN pip install uv && \
    uv pip compile pyproject.toml -o requirements.txt && \
    uv pip install --system -r requirements.txt

FROM python:3.14-alpine@sha256:9e9fde4d32eedce0b661d9ab91e826b62dddf28e928c230ec55f1866cac66b01

WORKDIR /app

COPY --from=builder /usr/local/lib/python3.14/site-packages /usr/local/lib/python3.14/site-packages
COPY main.py .

ENTRYPOINT ["python", "main.py"]

LABEL org.opencontainers.container.app="mcp-telegram"
