# Multi-stage build for the Sluice HTTP API server.

# --- build stage ---
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache module downloads separately from source for faster rebuilds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/sluice-server ./cmd/sluice-server

# --- runtime stage ---
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/sluice-server /app/sluice-server
EXPOSE 8080
USER nonroot:nonroot
ENTRYPOINT ["/app/sluice-server"]
CMD ["--addr", ":8080", "--data", "/data"]
