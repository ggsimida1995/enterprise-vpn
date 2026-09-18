FROM golang:1.22 AS build

WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN mkdir -p /out /data \
    && CGO_ENABLED=0 go build -trimpath -ldflags "-s -w" -o /out/enterprise-vpn-server . \
    && chown 65532:65532 /data \
    && touch /data/.keep

FROM gcr.io/distroless/static-debian12:nonroot

COPY --from=build /out/enterprise-vpn-server /enterprise-vpn-server
COPY --from=build --chown=65532:65532 /data /data
VOLUME ["/data"]
EXPOSE 8080
ENTRYPOINT ["/enterprise-vpn-server", "-addr", ":8080", "-config", "/data/server.json"]
