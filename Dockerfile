################################
# STEP 1 build executable binary
################################

FROM golang:1.15-alpine AS builder

# Install git + SSL ca certificates.
# Git is required for fetching the dependencies.
# Ca-certificates is required to call HTTPS endpoints.
RUN apk update && apk add --no-cache git ca-certificates tzdata \
    && update-ca-certificates 2>/dev/null || true

# Set the Current Working Directory inside the container
WORKDIR /web

# Copy go mod and sum files
COPY go.mod go.sum ./

# Download all dependencies. 
# Dependencies will be cached if the go.mod and go.sum files are not changed.
RUN go mod download

# Copy the source from the current directory to the Working Directory inside
# the container
COPY . .

# Build the go app.
RUN CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -a -installsuffix cgo \
 -ldflags '-extldflags "-static"' -o /go/bin/server .

############################
# STEP 2 build a small image
############################

FROM debian:stretch-slim as final
LABEL maintainer="https://github.com/saferwall"
LABEL version="0.1.0"
LABEL description="Saferwall web api service"

WORKDIR /backend

# Copy the app
COPY --from=builder /go/bin/server .
COPY ./data ./data
COPY ./app/schema ./app/schema

# Run the server.
ENTRYPOINT ["/backend/server"]
