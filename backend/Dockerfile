# Multi-stage build.
#
# The result is a distroless image containing one static binary and nothing
# else — no shell, no package manager, no libc. That is not minimalism for its
# own sake: a container with no shell is a container an attacker who achieves
# code execution cannot easily pivot from, and the attack surface of the base
# image is the part of a deployment nobody remembers to patch.

FROM golang:1.24-alpine AS build

WORKDIR /src

# Dependencies are copied and downloaded before the source, so a code change
# does not invalidate the module-download layer. This is the difference between
# a 5-second rebuild and a 2-minute one.
COPY go.mod go.sum ./
RUN go mod download

COPY . .

# CGO_ENABLED=0 produces a static binary that runs on a base image with no libc.
# -trimpath strips local filesystem paths from the binary; -s -w drop the symbol
# and DWARF tables. Together they cut the binary substantially and stop it
# leaking the build machine's directory layout in a stack trace.
RUN CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags='-s -w' \
      -o /out/api ./cmd/api \
 && CGO_ENABLED=0 GOOS=linux go build \
      -trimpath \
      -ldflags='-s -w' \
      -o /out/seed ./cmd/seed

FROM gcr.io/distroless/static-debian12:nonroot

# The tz database is required: SLA business-hours arithmetic loads IANA zones by
# name, and distroless ships without them. Without this every calendar silently
# falls back to UTC and every deadline is wrong by the local offset.
COPY --from=build /usr/local/go/lib/time/zoneinfo.zip /usr/local/go/lib/time/zoneinfo.zip
ENV ZONEINFO=/usr/local/go/lib/time/zoneinfo.zip

COPY --from=build /out/api /app/api
COPY --from=build /out/seed /app/seed

# Runs as an unprivileged user by default in this base image.
USER nonroot:nonroot

EXPOSE 8080
ENTRYPOINT ["/app/api"]
