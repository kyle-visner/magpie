# syntax=docker/dockerfile:1
FROM --platform=$BUILDPLATFORM golang:1.26.5-alpine AS build

ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod ./
COPY . .
RUN CGO_ENABLED=0 GOOS=$TARGETOS GOARCH=$TARGETARCH go build \
    -trimpath -ldflags="-s -w" -o /out/magpie ./cmd/magpie

FROM scratch
COPY --from=build /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=build /out/magpie /magpie
USER 65532:65532
EXPOSE 8787
ENTRYPOINT ["/magpie"]
CMD ["mcp", "--http", ":8787"]
