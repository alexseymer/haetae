ARG VERSION=dev

FROM node:20-alpine AS web
WORKDIR /web
COPY web/package.json web/package-lock.json ./
RUN npm ci
COPY web/ ./
RUN npm run build

FROM golang:1.27.3-alpine AS go
ARG VERSION=dev
ARG TARGETOS=linux
ARG TARGETARCH=amd64
WORKDIR /src
RUN apk add --no-cache ca-certificates
COPY go.mod go.sum ./
RUN go mod download
COPY . .
COPY --from=web /web/dist/. internal/webui/app/
RUN mkdir -p /var/lib/haetae /dist /out \
	&& CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
		-ldflags "-s -w -X github.com/alexseymer/haetae/internal/version.Version=${VERSION}" \
		-o /out/haetae-server ./cmd/haetae-server \
	&& CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath \
		-ldflags "-s -w -X github.com/alexseymer/haetae/internal/version.Version=${VERSION}" \
		-o /dist/haetae-agent-linux-amd64 ./cmd/haetae-agent \
	&& CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath \
		-ldflags "-s -w -X github.com/alexseymer/haetae/internal/version.Version=${VERSION}" \
		-o /dist/haetae-agent-linux-arm64 ./cmd/haetae-agent \
	&& cd /dist && sha256sum haetae-agent-linux-amd64 haetae-agent-linux-arm64 > SHA256SUMS

FROM gcr.io/distroless/static-debian12:nonroot
ARG VERSION=dev
LABEL org.opencontainers.image.source="https://github.com/alexseymer/haetae" \
	org.opencontainers.image.version="${VERSION}" \
	org.opencontainers.image.licenses="AGPL-3.0-only"
COPY --from=go /out/haetae-server /usr/local/bin/haetae-server
COPY --from=go /dist/ /usr/share/haetae/dist/
COPY --from=go --chown=65532:65532 /var/lib/haetae /var/lib/haetae
USER 65532:65532
ENV HAETAE_DATA_DIR=/var/lib/haetae \
	HAETAE_DIST_DIR=/usr/share/haetae/dist
EXPOSE 8080 8443
ENTRYPOINT ["/usr/local/bin/haetae-server", "serve"]
HEALTHCHECK --interval=5s --timeout=3s --start-period=20s --retries=12 CMD ["/usr/local/bin/haetae-server", "healthcheck"]
