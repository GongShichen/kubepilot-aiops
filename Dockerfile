FROM golang:1.26.2-alpine3.23 AS build
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY . .
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kubepilot-server ./cmd/server && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/kubepilot-benchmark ./cmd/benchmark && \
    CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /out/demo-service ./cmd/demo-service

FROM gcr.io/distroless/static-debian12:nonroot
COPY --from=build /out/kubepilot-server /usr/local/bin/kubepilot-server
COPY --from=build /out/kubepilot-benchmark /usr/local/bin/kubepilot-benchmark
COPY --from=build /out/demo-service /usr/local/bin/demo-service
COPY benchmark /app/benchmark
WORKDIR /app
USER nonroot:nonroot
EXPOSE 8080
ENTRYPOINT ["/usr/local/bin/kubepilot-server"]
