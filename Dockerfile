FROM golang:1.25 AS builder

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 GOOS=linux go build -ldflags="-s -w" -o /antiauto .

# Build playwright CLI for browser installation
RUN go build -ldflags="-s -w" -o /pw-install github.com/playwright-community/playwright-go/cmd/playwright

# ── Runtime ──
FROM ubuntu:24.04

RUN apt-get update && apt-get install -y --no-install-recommends \
    ca-certificates tzdata \
    # Playwright Chromium dependencies
    libnss3 libatk1.0-0 libatk-bridge2.0-0 libcups2 libdrm2 \
    libxkbcommon0 libxcomposite1 libxdamage1 libxrandr2 libgbm1 \
    libpango-1.0-0 libcairo2 libasound2t64 libxshmfence1 \
    libgtk-3-0 libx11-xcb1 fonts-liberation \
    && rm -rf /var/lib/apt/lists/*

ENV TZ=Asia/Shanghai
RUN ln -sf /usr/share/zoneinfo/$TZ /etc/localtime && echo $TZ > /etc/timezone

WORKDIR /app

COPY --from=builder /antiauto .
COPY --from=builder /pw-install /tmp/pw-install
COPY config.yaml.example data/config.yaml.example
# Also keep a copy outside the VOLUME so it survives mount
COPY config.yaml.example /defaults/config.yaml.example

# Pre-install Playwright Chromium into the image
RUN /tmp/pw-install install chromium && rm /tmp/pw-install

# Create mount-point directories
RUN mkdir -p data auths

EXPOSE 8080

# Mount points:
#   /app/data   — config.yaml + SQLite database (data.db)
#   /app/auths  — OAuth credential files
VOLUME ["/app/data", "/app/auths"]

CMD ["./antiauto"]
