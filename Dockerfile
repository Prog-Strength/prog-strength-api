# Build stage
# Pinned to a specific patch (not the floating 1.25-alpine tag) so a
# docker build always picks up the Go version we've verified clean
# against the latest govulncheck advisories. Bump in lockstep with
# the `go` directive in go.mod; CI's setup-go reads go.mod and will
# install the matching toolchain.
FROM golang:1.25.12-alpine AS builder

# Install build dependencies for CGo (required for go-sqlite3).
# sqlite-dev provides sqlite3.h, which the sqlite-vec cgo bindings #include.
RUN apk add --no-cache gcc musl-dev sqlite-dev

WORKDIR /build

# Copy go mod files first for better caching
COPY go.mod go.sum ./
RUN go mod download

# Copy source code
COPY . .

# APP_VERSION is injected at build time by the release pipeline (e.g. "v1.2.3").
# Local docker builds without --build-arg get "dev". Surfaces in every API
# response's "version" field via internal/version.Version.
ARG APP_VERSION=dev

# sqlite-vec.c (vendored by the asg017/sqlite-vec-go-bindings cgo package)
# references the BSD type aliases u_int8_t / u_int16_t / u_int64_t. glibc
# exposes these via <sys/types.h>, but musl (Alpine) does not — so the cgo
# compile fails with "unknown type name 'u_int8_t'". Map them to the standard
# <stdint.h> names for the C compiler. Scoped to this builder stage; the
# runtime stage below is a fresh image and is unaffected.
ENV CGO_CFLAGS="-Du_int8_t=uint8_t -Du_int16_t=uint16_t -Du_int32_t=uint32_t -Du_int64_t=uint64_t"

# Build the binary.
# CGO_ENABLED=1 is required for go-sqlite3.
# -ldflags injects the version string into internal/version.Version.
RUN CGO_ENABLED=1 GOOS=linux go build \
    -a -installsuffix cgo \
    -ldflags="-X github.com/Prog-Strength/prog-strength-api/internal/version.Version=${APP_VERSION}" \
    -o api ./cmd/api

# TEMPORARY (weather backfill rollout, sows/activity-weather-conditions.md).
# Ships the operator command alongside the server so `docker exec` can run it
# against the live /data/app.db with a binary and a schema that are guaranteed
# to match — the whole reason it lives in the image rather than being built by
# hand on the host, where a checkout ahead of APP_VERSION would apply unshipped
# migrations to prod out of band.
#
# Building it does NOT run it: CMD below is still `./api` alone, so this binary
# is inert on every deploy until someone execs it deliberately.
#
# No -a/-installsuffix here (unlike the api build above): those force a full
# rebuild of every dependency, and omitting them lets this reuse the api's
# build cache — a couple of seconds instead of a second cold compile.
#
# TO REMOVE once the backfill is done: delete this RUN, the COPY in the runtime
# stage below, and cmd/weather-backfill/. Nothing else references it.
RUN CGO_ENABLED=1 GOOS=linux go build -o weather-backfill ./cmd/weather-backfill

# Runtime stage
FROM alpine:latest

# Install runtime dependencies
RUN apk --no-cache add ca-certificates

WORKDIR /app

# Copy the binary from builder
COPY --from=builder /build/api .

# TEMPORARY — paired with the weather-backfill build above; remove together.
COPY --from=builder /build/weather-backfill .

# Create data directory for SQLite database
RUN mkdir -p /data

# Expose port
EXPOSE 8080

# Set environment variables with defaults
ENV DATABASE_URL=/data/app.db
ENV SERVER_ADDR=:8080

# Run the application
CMD ["./api"]
