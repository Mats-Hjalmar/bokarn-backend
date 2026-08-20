# syntax=docker/dockerfile:1

FROM golang:1.26-alpine AS builder

WORKDIR /src

ENV GOWORK=off
ENV CGO_ENABLED=0

COPY go.mod go.sum ./
RUN go mod download

COPY . .

RUN go build -trimpath -ldflags="-s -w" -o /out/bokarn ./cmd/api
RUN go build -trimpath -ldflags="-s -w" -o /out/migrate ./cmd/migrate
RUN go build -trimpath -ldflags="-s -w" -o /out/job ./cmd/job

FROM alpine:3.21

RUN apk add --no-cache ca-certificates wget \
	&& adduser -D -u 10001 app

COPY --from=builder /out/bokarn /usr/local/bin/bokarn
COPY --from=builder /out/migrate /usr/local/bin/migrate
COPY --from=builder /out/job /usr/local/bin/job

USER app
EXPOSE 1437

HEALTHCHECK --interval=10s --timeout=3s --start-period=5s --retries=5 \
	CMD wget -qO- http://localhost:1437/api/v1/healthz || exit 1

CMD ["bokarn"]
