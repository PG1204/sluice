# Multi-stage build for the Sluice API server.
#
# STUB: the API server binary (cmd target ./api) doesn't exist yet — it lands
# in Phase 7. Until then this builds the CLI so the image is valid and the
# build pipeline can be exercised. Swap the build target to ./api when it ships.

# --- build stage ---
FROM golang:1.26-alpine AS build
WORKDIR /src

# Cache module downloads separately from source for faster rebuilds.
COPY go.mod go.sum ./
RUN go mod download

COPY . .
RUN CGO_ENABLED=0 go build -o /out/sluice ./cli

# --- runtime stage ---
FROM gcr.io/distroless/static-debian12:nonroot
WORKDIR /app
COPY --from=build /out/sluice /app/sluice
USER nonroot:nonroot
ENTRYPOINT ["/app/sluice"]
