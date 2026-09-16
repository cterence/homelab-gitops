# Build stage: Kaniko clones the repo into a scratch context and runs this
# Dockerfile, so the source tree is available. Dependencies are compiled and
# installed into site-packages, then copied into the runtime image.
FROM python:3.13-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a AS builder

WORKDIR /app

COPY pyproject.toml .

RUN apk add --no-cache ffmpeg && \
    pip install uv && \
    uv pip compile pyproject.toml -o requirements.txt && \
    uv pip install --system -r requirements.txt

FROM python:3.13-alpine@sha256:7415fbc3c9e4979cc717d92377ab2bc7b2b4a2af1ac03cc52b5f3f88efedaf3a

WORKDIR /app

RUN apk add --no-cache ffmpeg

COPY --from=builder /usr/local/lib/python3.13/site-packages /usr/local/lib/python3.13/site-packages
COPY main.py .

ENTRYPOINT ["python", "main.py"]

LABEL org.opencontainers.container.app="tts9000"
