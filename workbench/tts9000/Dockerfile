# Build stage: Kaniko clones the repo into a scratch context and runs this
# Dockerfile, so the source tree is available. Dependencies are compiled and
# installed into site-packages, then copied into the runtime image.
FROM python:3.14-alpine@sha256:c6ead215bfd31f1e433d968853b7a769989117115b728874824e6c0a27cb96fc AS builder

WORKDIR /app

COPY pyproject.toml .

RUN apk add --no-cache ffmpeg && \
    pip install uv && \
    uv pip compile pyproject.toml -o requirements.txt && \
    uv pip install --system -r requirements.txt

FROM python:3.14-alpine@sha256:c6ead215bfd31f1e433d968853b7a769989117115b728874824e6c0a27cb96fc

WORKDIR /app

RUN apk add --no-cache ffmpeg

COPY --from=builder /usr/local/lib/python3.13/site-packages /usr/local/lib/python3.13/site-packages
COPY main.py .

ENTRYPOINT ["python", "main.py"]

LABEL org.opencontainers.container.app="tts9000"
